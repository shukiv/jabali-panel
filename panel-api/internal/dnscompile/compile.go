// Package dnscompile produces the DNS record list the agent will write
// to PowerDNS. The input is a zone, its records, and the server-wide
// settings (nameserver names/IPs). The output is a flat slice of
// records in agent wire format.
package dnscompile

import (
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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
	out := SystemRecords(zone, srv)

	// Operator-editable records.
	for _, r := range records {
		if r.Type == "SOA" {
			continue // We own SOA.
		}
		if r.Type == "NS" && r.Managed {
			continue // Managed NS — regenerated above.
		}
		name := expandName(r.Name, zoneName)
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
