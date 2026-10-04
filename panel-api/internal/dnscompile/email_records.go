package dnscompile

import (
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// EmailRecordsManagedBy is the marker stamped into dns_records.managed_by
// for every record this file emits. The disable path uses it as the
// WHERE clause to scope cleanup — M4 bootstrap records (A / MX / SPF /
// DMARC) get NULL at create time via BootstrapRecords, so the M6
// delete-on-disable query can't accidentally touch them.
const EmailRecordsManagedBy = "m6"

// EmailRecordsSelector is the hardcoded DKIM selector used for v1.
// ADR-0043 carries a selector column on domains for future rotation;
// until we ship rotation, every domain uses the same "jabali" label.
const EmailRecordsSelector = "jabali"

// BuildEmailRecords returns the per-domain DNS records inserted on
// domain.email_enable beyond what M4's BootstrapRecords already put
// in place at domain-create time.
//
// What M4 already installs (via BootstrapRecords, flagged Managed=true
// but ManagedBy=NULL):
//
//	@       A    <server ipv4>
//	www     A    <server ipv4>
//	mail    A    <server ipv4>
//	@       MX   mail                        (priority 10)
//	@       TXT  "v=spf1 mx ~all"
//	_dmarc  TXT  "v=DMARC1; p=none"
//
// What M6 adds (flagged Managed=true, ManagedBy="m6"):
//
//	jabali._domainkey  TXT    "v=DKIM1; k=ed25519; p=<pubkey>"
//	autoconfig         CNAME  mail
//	autodiscover       CNAME  mail
//	_autodiscover._tcp SRV    0 0 443 mail   (priority in the column, see below)
//	_caldav(s)/_carddav(s)._tcp SRV ...
//	_imap/_imaps/_submission/_submissions._tcp SRV ...  (RFC 6186)
//	_smtp._tls         TXT    "v=TLSRPTv1; rua=mailto:postmaster@<zone>"
//	@                  CAA    0 issue "letsencrypt.org"
//	@                  CAA    0 iodef "mailto:postmaster@<zone>"
//
// The DKIM/autoconfig/SRV set plus the GH #134 additions (client-
// service SRVs, TLS-RPT, CAA, autodiscover) — the apex A/MX/SPF/DMARC
// exist already and rewriting them would (a) invalidate any operator
// edit (the entire point of ManagedBy scoping is to preserve
// overrides) and (b) churn PowerDNS unnecessarily. If an install
// somehow has a domain with NO M4 bootstrap records (imported zone
// file, older panel version), the reconciler is the place to re-apply
// them — not the email-enable handler, which has a narrower remit.
//
// Contract notes:
//   - CNAME content is the FQDN "mail.<zone>". PowerDNS serves record
//     content verbatim — a short label "mail" would be sent as a
//     root-relative "mail." that clients can't resolve. Matches the
//     www-CNAME-to-apex convention established in BootstrapRecords.
//   - SRV content is "priority weight port target" per RFC 2782.
//     Target is the FQDN "mail.<zone>" for the same reason.
//   - TXT content is double-quoted to match BootstrapRecords' format so
//     a textual diff between the two won't flap on reconciliation.
func BuildEmailRecords(
	zoneID, zoneName, selector, dkimPublicKey string,
	srv *models.ServerSettings,
	idNew func() string,
	now time.Time,
) []models.DNSRecord {
	m6 := EmailRecordsManagedBy
	mk := func(name, typ, content string, priority int) models.DNSRecord {
		return models.DNSRecord{
			ID:        idNew(),
			ZoneID:    zoneID,
			Name:      name,
			Type:      typ,
			Content:   content,
			TTL:       models.EffectiveDNSTTL(srv),
			Priority:  priority,
			Managed:   true,
			ManagedBy: &m6,
			IsEnabled: true,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	mailTarget := "mail." + zoneName
	// SRV content is "weight port target" — the PRIORITY belongs in the
	// priority column, never in the content string, exactly as MX is written
	// (`mk("@", "MX", "mail."+zone, 10)`).
	//
	// These carried the priority in BOTH places. PowerDNS's gmysql backend on
	// this schema prepends the prio column to the content for MX and SRV, so
	// "0 1 465 mail.example.com" became a five-field record:
	//
	//	pdnsutil check-zone:
	//	  Error was: When parsing SRV trailing data was not parsed: ' mail...'
	//
	// and pdns refused to serve it. Every one of these nine records returned
	// an EMPTY answer — verified with dig against the authoritative server —
	// which silently disables SRV-based mail client autoconfiguration
	// (Thunderbird, Outlook, iOS) and CalDAV/CardDAV discovery on every
	// email-enabled domain. MX was unaffected because it was already written
	// the correct way.
	//
	// No migration needed: the agent's pdns client DELETEs and reinserts the
	// whole zone on each push, so hosts converge on the next reconcile.
	return []models.DNSRecord{
		mk(selector+"._domainkey", "TXT", `"`+dkimPublicKey+`"`, 0),
		mk("autoconfig", "CNAME", mailTarget, 0),
		mk("_autodiscover._tcp", "SRV", "0 443 "+mailTarget, 0),
		mk("_caldavs._tcp", "SRV", "1 443 "+mailTarget, 0),
		mk("_carddavs._tcp", "SRV", "1 443 "+mailTarget, 0),
		mk("_caldav._tcp", "SRV", "1 80 "+mailTarget, 0),
		mk("_carddav._tcp", "SRV", "1 80 "+mailTarget, 0),
		// --- GH #134: full Stalwart-recommended mail record set ---
		// autodiscover CNAME — the Outlook/Exchange autodiscovery
		// flavour, alongside the _autodiscover._tcp SRV above.
		mk("autodiscover", "CNAME", mailTarget, 0),
		// Client-service SRV records (RFC 6186) — let MUAs auto-discover
		// the IMAP + submission ports Stalwart listens on. weight 1 so a
		// resolver picks them deterministically.
		mk("_imap._tcp", "SRV", "1 143 "+mailTarget, 0),
		mk("_imaps._tcp", "SRV", "1 993 "+mailTarget, 0),
		mk("_submission._tcp", "SRV", "1 587 "+mailTarget, 0),
		mk("_submissions._tcp", "SRV", "1 465 "+mailTarget, 0),
		// TLS-RPT (RFC 8460) — where receivers send aggregate reports of
		// TLS negotiation failures delivering to this domain.
		mk("_smtp._tls", "TXT", `"v=TLSRPTv1; rua=mailto:postmaster@`+zoneName+`"`, 0),
		// CAA — restrict certificate issuance to Let's Encrypt (the CA
		// jabali uses for both web + mail certs) and publish an incident-
		// reporting address. Two records form one CAA RRset; PowerDNS
		// stores them as distinct rows under the apex.
		mk("@", "CAA", `0 issue "letsencrypt.org"`, 0),
		mk("@", "CAA", `0 iodef "mailto:postmaster@`+zoneName+`"`, 0),
	}
}

// DAVSecureSRV is the content of a domain's secure CalDAV or CardDAV SRV row
// (_caldavs._tcp / _carddavs._tcp): "<weight> <port> <target>", with the
// priority in its own column. With no override it points at mailTarget on 443.
// With a per-domain override (GH #1462, "host" or "host:port") it points at
// that host, on its port or 443. The reconciler writes this row and the mail
// DNS hints show it (GH #1917), so the two cannot drift.
func DAVSecureSRV(override, mailTarget string) string {
	if strings.TrimSpace(override) == "" {
		return "1 443 " + mailTarget
	}
	host, port := splitDAVHostPort(override, 443)
	return "1 " + strconv.Itoa(port) + " " + host
}

// splitDAVHostPort parses a validated "host" or "host:port" override into
// (host, port), defaulting the port. Input is API-validated (davHostRe), so
// this only has to separate a trailing :port; a malformed value can't reach it.
func splitDAVHostPort(override string, defPort int) (string, int) {
	override = strings.TrimSpace(override)
	if i := strings.LastIndexByte(override, ':'); i > 0 {
		if p, err := strconv.Atoi(override[i+1:]); err == nil && p > 0 && p <= 65535 {
			return override[:i], p
		}
	}
	return override, defPort
}
