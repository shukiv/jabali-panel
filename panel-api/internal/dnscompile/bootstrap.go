package dnscompile

import (
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// BootstrapRecords returns the default records created on new-domain
// provisioning. All flagged Managed=true so the UI can mark them
// read-only; ManagedBy is left NULL so the M6 email-disable cleanup
// (which scopes deletes by managed_by="m6") can't touch them.
//
// Record shape:
//
//	@     A     <server ipv4>
//	@     AAAA  <server ipv6>           (when configured)
//	mail  A     <server ipv4>           (MX target — must stay an A, RFC 2181 §10.3)
//	mail  AAAA  <server ipv6>
//	www   CNAME @                       (follows apex; no duplicate IP)
//	@     MX    mail                    (priority 10)
//	@     TXT   "v=spf1 mx ip4:<v4> [ip6:<v6>] ~all"
//	_dmarc TXT  "v=DMARC1; p=none"
//
// Notes:
//   - www is a CNAME to the apex so an apex-IP change propagates without
//     a per-record rewrite. mail deliberately stays an A because MX
//     targets MUST NOT be CNAME aliases (RFC 2181 §10.3) — some mail
//     servers reject such zones with SERVFAIL.
//   - SPF includes explicit ip4/ip6 beyond the `mx` directive so mail
//     sent from the apex IP (not just from the mail host's A record)
//     still passes SPF checks — e.g. panel-local scripts sending via
//     the local stalwart over the apex bind.
func BootstrapRecords(zoneID, zoneName string, srv *models.ServerSettings, idNew func() string, includeMail, includeWWW bool) []models.DNSRecord {
	now := time.Now().UTC()
	// GH #527: honor server_settings.default_dns_ttl for bootstrap records
	// instead of a hardcoded value, so the operator's configured default
	// actually applies to the apex / mail / NS rows a new zone starts with.
	defTTL := models.EffectiveDNSTTL(srv)
	mk := func(name, typ, content string, priority int) models.DNSRecord {
		return models.DNSRecord{
			ID:        idNew(),
			ZoneID:    zoneID,
			Name:      name,
			Type:      typ,
			Content:   content,
			TTL:       defTTL,
			Priority:  priority,
			Managed:   true,
			IsEnabled: true,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	var out []models.DNSRecord
	if srv == nil {
		return out
	}

	// Apex + mail host IPs. www is added as a CNAME below.
	if srv.PublicIPv4 != "" {
		out = append(out, mk("@", "A", srv.PublicIPv4, 0))
		if includeMail {
			out = append(out, mk("mail", "A", srv.PublicIPv4, 0))
		}
	}
	if srv.PublicIPv6 != "" {
		out = append(out, mk("@", "AAAA", srv.PublicIPv6, 0))
		if includeMail {
			out = append(out, mk("mail", "AAAA", srv.PublicIPv6, 0))
		}
	}

	// www CNAME to the apex. Content is the zone FQDN verbatim (pdns
	// treats a no-dot content as fully-qualified — it does NOT append
	// the zone). Writing the zone name instead of a short label keeps
	// the reconciler's wire-to-pdns pass a pure passthrough — Compile
	// doesn't expand CNAME content today and adding that expansion
	// would implicitly rewrite operator-edited CNAMEs too.
	// www CNAME is opt-in per domain (GH #225). Unchecked by default at
	// create — the operator ticks "Create www record" when they want it.
	// Gated on zoneName for the same broken-content reason as the MX row.
	if includeWWW && zoneName != "" {
		out = append(out, mk("www", "CNAME", zoneName, 0))
	}

	// MX target is the FQDN "mail.<zone>". PowerDNS serves record
	// content verbatim — a short-label "mail" would be sent on wire as
	// a root-relative "mail." that clients interpret as a TLD, failing
	// to resolve. Same convention as the www CNAME above. Skip the row
	// entirely if we don't know the zone name rather than write broken
	// content.
	// Mail rows (MX / SPF / DMARC) only when this domain uses Jabali mail.
	// For provider=none / external (m365/google) we skip them so a "No mail"
	// domain never even briefly has Jabali mail DNS (GH #189); the
	// mail-provider reconciler publishes provider-specific rows where needed.
	if includeMail {
		if zoneName != "" {
			out = append(out, mk("@", "MX", "mail."+zoneName, 10))
		}
		out = append(out, mk("@", "TXT", BuildSPFString(srv), 0))
		out = append(out, mk("_dmarc", "TXT", `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`, 0))
	}

	// ns1/ns2 A records. compile.go already synthesises the NS records
	// at the zone apex from server_settings.ns1_name / ns2_name, but
	// without A records for those nameservers, resolvers get NXDOMAIN
	// for the in-zone "ns1.<zone>" / "ns2.<zone>" lookups. Whenever
	// the configured nameserver hostname IS a label inside this zone
	// (e.g. zone "example.com", ns1_name "ns1.example.com"), seed the
	// matching A record now. Skip when the nameserver lives in a
	// different zone (e.g. ns1 hosted off-site) — the operator owns
	// that record elsewhere.
	if zoneName != "" {
		appendNSARecord(&out, mk, zoneName, srv.NS1Name, srv.NS1IPv4)
		appendNSARecord(&out, mk, zoneName, srv.NS2Name, srv.NS2IPv4)
	}

	return out
}

// appendNSARecord emits an A record for the nameserver hostname if the
// hostname lives inside zoneName. Idempotent: noop on empty inputs or
// when the nameserver is hosted elsewhere.
func appendNSARecord(out *[]models.DNSRecord, mk func(name, typ, content string, priority int) models.DNSRecord, zoneName, nsName, nsIPv4 string) {
	if nsName == "" || nsIPv4 == "" || zoneName == "" {
		return
	}
	suffix := "." + zoneName
	if !strings.HasSuffix(nsName, suffix) {
		return
	}
	label := strings.TrimSuffix(nsName, suffix)
	if label == "" {
		return
	}
	*out = append(*out, mk(label, "A", nsIPv4, 0))
}

// BuildSPFString renders the bootstrap SPF TXT content from server
// settings. Always returns a quoted, trailing-"~all" SPF record with
// the `mx` directive plus any configured ip4/ip6 directives. Exported
// so the reconciler's legacy-shape migration can compute the exact
// string it expects to write without duplicating the format.
//
// Examples:
//
//	srv{v4=1.2.3.4}            → `"v=spf1 mx ip4:1.2.3.4 ~all"`
//	srv{v4=1.2.3.4, v6=2001::1} → `"v=spf1 mx ip4:1.2.3.4 ip6:2001::1 ~all"`
//	srv{} (no IPs)              → `"v=spf1 mx ~all"` (matches the
//	                               pre-migration bootstrap shape; the
//	                               migration uses this fact as the
//	                               legacy-content sentinel).
func BuildSPFString(srv *models.ServerSettings) string {
	var spf strings.Builder
	spf.WriteString(`"v=spf1 mx`)
	if srv != nil && srv.PublicIPv4 != "" {
		spf.WriteString(" ip4:")
		spf.WriteString(srv.PublicIPv4)
	}
	if srv != nil && srv.PublicIPv6 != "" {
		spf.WriteString(" ip6:")
		spf.WriteString(srv.PublicIPv6)
	}
	spf.WriteString(` ~all"`)
	return spf.String()
}

// LegacyBootstrapSPFContent is the exact SPF TXT value BootstrapRecords
// wrote before the ip4/ip6 change. The migration uses it as the
// sentinel to decide whether a row was operator-edited or is the
// pristine pre-migration bootstrap content. A single character of
// drift (extra space, a different spf modifier) is enough to mark the
// row as operator-touched and skip the rewrite.
const LegacyBootstrapSPFContent = `"v=spf1 mx ~all"`
