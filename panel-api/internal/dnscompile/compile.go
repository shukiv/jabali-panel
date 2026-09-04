// Package dnscompile produces the DNS record list the agent will write
// to PowerDNS. The input is a zone, its records, and the server-wide
// settings (nameserver names/IPs). The output is a flat slice of
// records in agent wire format.
package dnscompile

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/idna"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// dnsNameIDNA is a lenient IDNA profile used ONLY to punycode-encode
// labels that actually contain non-ASCII runes. Unlike idna.Lookup
// (which mailaddr uses for e-mail domains) it does NOT enforce STD3
// rules, because DNS record names legitimately carry underscores
// (_dmarc, _domainkey, _acme-challenge, SRV _service._proto) and the
// wildcard label "*". We never feed it an ASCII label, so its sole job
// is Unicode→ASCII on genuine IDN labels.
var dnsNameIDNA = idna.New(
	idna.MapForLookup(),
	idna.Transitional(false),
	idna.StrictDomainName(false),
)

// Record is the wire shape the agent expects in dns.zone.upsert.
type Record struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
	Disabled bool   `json:"disabled"`
}

// SystemRecords returns the auto-generated SOA + NS records that
// Compile injects at the top of every rendered zone. Split out so the
// HTTP surface can expose them as a read-only block to the UI without
// returning operator-editable rows mixed in. Deterministic output
// given a (zone, srv) pair EXCEPT for SOA.Content's serial, which is
// wall-clock time — callers comparing two SystemRecords results
// across time should diff every field except that one.
func SystemRecords(zone *models.DNSZone, srv *models.ServerSettings) []Record {
	if zone == nil {
		return nil
	}
	zoneName := strings.TrimSuffix(zone.Name, ".")
	// GH #1459: punycode a raw-IDN apex so the SOA/NS/apex-A record
	// names match the (also-normalized) domains.name the reconciler
	// pushes, and stay latin1-safe for PowerDNS's records.name column.
	// Identity for ordinary ASCII zones — no output change, no hash churn.
	if nz, ok := NormalizeName(zoneName); ok {
		zoneName = nz
	}
	// GH #527: apex SOA + NS TTLs follow the operator default like every other
	// record (the SOA content refresh/retry/expire/minimum timers are unchanged).
	sysTTL := models.EffectiveDNSTTL(srv)

	// SOA is generated from server_settings + zone scalars, never from
	// the dns_records table directly. That keeps SOA consistent even
	// if an operator accidentally deletes the row.
	serial := time.Now().UTC().Unix()
	if zone.Serial > serial {
		serial = zone.Serial + 1
	}
	primaryNS := zoneName
	if srv != nil && srv.NS1Name != "" {
		primaryNS = srv.NS1Name
	}
	hostmaster := "hostmaster." + zoneName // Standard default; admins can override via settings later.
	if srv != nil && srv.AdminEmail != "" {
		hostmaster = emailToSOAHostmaster(srv.AdminEmail)
	}
	out := []Record{{
		Name: zoneName,
		Type: "SOA",
		Content: fmt.Sprintf("%s %s %d %d %d %d %d",
			primaryNS, hostmaster, serial,
			zone.RefreshSeconds, zone.RetrySeconds, zone.ExpireSeconds, zone.MinimumTTL),
		TTL: sysTTL,
	}}

	// NS records — one per configured nameserver. Without server_settings
	// we still emit the zone apex as its own NS so PowerDNS accepts the
	// zone as valid.
	if srv == nil || srv.NS1Name == "" {
		out = append(out, Record{Name: zoneName, Type: "NS", Content: zoneName, TTL: sysTTL})
	} else {
		out = append(out, Record{Name: zoneName, Type: "NS", Content: srv.NS1Name, TTL: sysTTL})
		if srv.NS2Name != "" {
			out = append(out, Record{Name: zoneName, Type: "NS", Content: srv.NS2Name, TTL: sysTTL})
		}
	}
	return out
}

// Compile flattens the zone into the wire format. Serial is derived
// from UpdatedAt — bumping it on every push is PowerDNS convention.
func Compile(zone *models.DNSZone, records []models.DNSRecord, srv *models.ServerSettings) []Record {
	zoneName := strings.TrimSuffix(zone.Name, ".")
	if nz, ok := NormalizeName(zoneName); ok {
		zoneName = nz // keep expandName's suffix in sync with the apex above
	}
	out := SystemRecords(zone, srv)

	// Operator-editable records.
	for _, r := range records {
		if r.Type == "SOA" {
			continue // We own SOA.
		}
		if r.Type == "NS" && r.Managed {
			continue // Managed NS — regenerated above.
		}
		name, ok := NormalizeName(expandName(r.Name, zoneName))
		if !ok {
			// GH #1459: a name we can't store (non-encodable IDN /
			// over-length / malformed) would fail the latin1 records.name
			// INSERT and roll back the ENTIRE dns.zone.upsert transaction,
			// silently dropping the whole zone from PowerDNS. Skip just
			// this one record so the rest of the zone still provisions.
			continue
		}
		out = append(out, Record{
			Name:     name,
			Type:     r.Type,
			Content:  r.Content,
			TTL:      r.TTL,
			Priority: r.Priority,
			Disabled: !r.IsEnabled,
		})
	}

	return out
}

// NormalizeName renders a DNS name into the ASCII, latin1-safe form
// PowerDNS's records.name / domains.name columns require, punycode-
// encoding any label that carries non-ASCII runes and leaving every
// ASCII label byte-for-byte intact (so _dmarc/_domainkey/SRV
// underscores, the wildcard "*", hyphens and existing case are
// preserved, and ordinary zones compile identically — no zone-cache /
// push-hash churn).
//
// It returns (encoded, true) for a name that fits DNS limits (≤253
// octets overall, each label 1..63 octets) and ("", false) for one that
// is empty, has an empty/over-long label, or contains an IDN label that
// cannot be encoded. A false result is the caller's cue to SKIP that
// record rather than let it fail the atomic zone INSERT (GH #1459).
func NormalizeName(name string) (string, bool) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return "", false
	}
	labels := strings.Split(name, ".")
	for i, label := range labels {
		if label == "" {
			return "", false // empty label, e.g. "a..b"
		}
		if isASCIIStr(label) {
			if len(label) > 63 {
				return "", false
			}
			continue // ASCII label — preserve verbatim (case, "_", "*", "-")
		}
		enc, err := dnsNameIDNA.ToASCII(label)
		if err != nil || enc == "" || len(enc) > 63 {
			return "", false
		}
		labels[i] = enc
	}
	out := strings.Join(labels, ".")
	if len(out) > 253 {
		return "", false
	}
	return out, true
}

// isASCIIStr reports whether s is pure 7-bit ASCII.
func isASCIIStr(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// expandName converts panel-side names (@, short labels, FQDN) to the
// fully-qualified form PowerDNS wants.
func expandName(name, zone string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "@" {
		return zone
	}
	if strings.HasSuffix(name, ".") {
		return strings.TrimSuffix(name, ".")
	}
	if strings.HasSuffix(name, "."+zone) {
		return name
	}
	if name == zone {
		return zone
	}
	return name + "." + zone
}

// emailToSOAHostmaster converts "admin@example.com" to the SOA form
// "admin.example.com" (. replaces @). Escapes a literal . in the local
// part with a backslash per RFC 1035.
func emailToSOAHostmaster(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "hostmaster." + email
	}
	local := strings.ReplaceAll(email[:at], ".", `\.`)
	return local + "." + email[at+1:]
}
