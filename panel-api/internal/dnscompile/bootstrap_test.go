package dnscompile

import (
	"strconv"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// idCounter returns a deterministic id-generator for tests. Keeps assertions
// about record ordering readable without coupling to ULID time bits.
func bootIDCounter() func() string {
	n := 0
	return func() string {
		n++
		return "id-" + strconv.Itoa(n)
	}
}

// Find returns the first record matching (name,type). Fails the test if
// absent — every lookup in the bootstrap tests is load-bearing.
func findRec(t *testing.T, recs []models.DNSRecord, name, typ string) models.DNSRecord {
	t.Helper()
	for _, r := range recs {
		if r.Name == name && r.Type == typ {
			return r
		}
	}
	t.Fatalf("no record found for name=%q type=%q; got %d records", name, typ, len(recs))
	return models.DNSRecord{}
}

func TestBootstrapRecords_WWWIsCNAMEToApex(t *testing.T) {
	recs := BootstrapRecords(
		"zone1",
		"example.com",
		&models.ServerSettings{PublicIPv4: "192.0.2.1"},
		bootIDCounter(), true,
		true,
		true,
	)

	// www must be a CNAME, not an A — MX-target-can't-be-a-CNAME
	// consideration doesn't apply here (www isn't an MX target), and
	// the shape lets apex-IP changes propagate without rewrites.
	for _, r := range recs {
		if r.Name == "www" && r.Type == "A" {
			t.Fatalf("www must not have an A record after the CNAME migration; got %+v", r)
		}
	}
	www := findRec(t, recs, "www", "CNAME")
	if www.Content != "example.com" {
		t.Errorf("www CNAME content should be the apex FQDN, got %q", www.Content)
	}
}

func TestBootstrapRecords_MailStaysA_NotCNAME(t *testing.T) {
	// RFC 2181 §10.3: MX targets MUST NOT be CNAME aliases. The MX
	// record below points at "mail", so "mail" must be an A (+AAAA
	// when v6 is set), never a CNAME.
	recs := BootstrapRecords(
		"zone1",
		"example.com",
		&models.ServerSettings{PublicIPv4: "192.0.2.1", PublicIPv6: "2001:db8::1"},
		bootIDCounter(), true,
		true,
		true,
	)
	for _, r := range recs {
		if r.Name == "mail" && r.Type == "CNAME" {
			t.Fatalf("mail must not be a CNAME (RFC 2181 §10.3 — MX targets can't be aliases); got %+v", r)
		}
	}
	a := findRec(t, recs, "mail", "A")
	if a.Content != "192.0.2.1" {
		t.Errorf("mail A content wrong: %q", a.Content)
	}
	aaaa := findRec(t, recs, "mail", "AAAA")
	if aaaa.Content != "2001:db8::1" {
		t.Errorf("mail AAAA content wrong: %q", aaaa.Content)
	}
}

func TestBootstrapRecords_MXTargetIsFQDN(t *testing.T) {
	// Regression: MX target was the short label "mail", which PowerDNS
	// serves verbatim (does NOT auto-append the zone). Clients saw a
	// root-relative "mail." — a TLD lookup that fails. Target must be
	// the zone-qualified FQDN so resolvers reach the mail host.
	recs := BootstrapRecords(
		"zone1",
		"example.com",
		&models.ServerSettings{PublicIPv4: "192.0.2.1"},
		bootIDCounter(), true,
		true,
		true,
	)
	mx := findRec(t, recs, "@", "MX")
	if mx.Content != "mail.example.com" {
		t.Errorf("MX content must be FQDN, got %q (want %q)", mx.Content, "mail.example.com")
	}
	if mx.Priority != 10 {
		t.Errorf("MX priority must be 10, got %d", mx.Priority)
	}
}

func TestBootstrapRecords_MXSkippedWhenZoneNameEmpty(t *testing.T) {
	// Mirror of the www-CNAME safety: if zoneName is absent we can't
	// build an FQDN target, so skip emitting a broken MX row rather
	// than fall back to a short-label content that fails resolution.
	recs := BootstrapRecords(
		"zone1",
		"",
		&models.ServerSettings{PublicIPv4: "192.0.2.1"},
		bootIDCounter(), true,
		true,
		true,
	)
	for _, r := range recs {
		if r.Type == "MX" {
			t.Fatalf("MX record must not be emitted when zoneName is empty; got %+v", r)
		}
	}
}

func TestBootstrapRecords_SPFIncludesIP4AndIP6(t *testing.T) {
	tests := []struct {
		name   string
		v4, v6 string
		want   string
	}{
		{"v4 only", "192.0.2.1", "", `"v=spf1 mx ip4:192.0.2.1 ~all"`},
		{"v4 and v6", "192.0.2.1", "2001:db8::1", `"v=spf1 mx ip4:192.0.2.1 ip6:2001:db8::1 ~all"`},
		{"neither", "", "", `"v=spf1 mx ~all"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := BootstrapRecords(
				"zone1",
				"example.com",
				&models.ServerSettings{PublicIPv4: tt.v4, PublicIPv6: tt.v6},
				bootIDCounter(), true,
				true,
				true,
			)
			// SPF is the "@" TXT that starts with v=spf1.
			var spf string
			for _, r := range recs {
				if r.Name == "@" && r.Type == "TXT" && strings.Contains(r.Content, "v=spf1") {
					spf = r.Content
					break
				}
			}
			if spf == "" {
				t.Fatal("no SPF record found")
			}
			if spf != tt.want {
				t.Errorf("SPF mismatch:\n  want: %s\n  got:  %s", tt.want, spf)
			}
		})
	}
}

func TestBootstrapRecords_NoServerSettingsReturnsEmpty(t *testing.T) {
	recs := BootstrapRecords("zone1", "example.com", nil, bootIDCounter(), true, true, true)
	if len(recs) != 0 {
		t.Errorf("expected 0 records when srv is nil, got %d", len(recs))
	}
}

func TestBootstrapRecords_WWWSkippedWhenZoneNameEmpty(t *testing.T) {
	// Safety: if the caller somehow forgets to pass zoneName, we would
	// rather emit no www record than write an empty-content CNAME.
	recs := BootstrapRecords(
		"zone1",
		"",
		&models.ServerSettings{PublicIPv4: "192.0.2.1"},
		bootIDCounter(), true,
		true,
		true,
	)
	for _, r := range recs {
		if r.Name == "www" {
			t.Fatalf("www record must not be emitted when zoneName is empty; got %+v", r)
		}
	}
}

func TestBootstrapRecords_AllManagedTrue_ManagedByNil(t *testing.T) {
	// Contract: Managed=true lets the UI mark the rows read-only;
	// ManagedBy=nil is what keeps the email-disable cleanup
	// (WHERE managed_by="m6") from touching them.
	recs := BootstrapRecords(
		"zone1",
		"example.com",
		&models.ServerSettings{PublicIPv4: "192.0.2.1"},
		bootIDCounter(), true,
		true,
		true,
	)
	for _, r := range recs {
		if !r.Managed {
			t.Errorf("record %s %s should be Managed=true", r.Name, r.Type)
		}
		if r.ManagedBy != nil {
			t.Errorf("record %s %s should have ManagedBy=nil, got %v", r.Name, r.Type, *r.ManagedBy)
		}
	}
}

func TestBootstrapRecords_NSARecord_InZone(t *testing.T) {
	srv := &models.ServerSettings{
		PublicIPv4: "203.0.113.10",
		NS1Name:    "ns1.example.com",
		NS1IPv4:    "203.0.113.10",
		NS2Name:    "ns2.example.com",
		NS2IPv4:    "203.0.113.11",
	}
	recs := BootstrapRecords("zone-1", "example.com", srv, bootIDCounter(), true, true, true)
	ns1 := findRec(t, recs, "ns1", "A")
	if ns1.Content != "203.0.113.10" {
		t.Errorf("ns1 A content = %q, want 203.0.113.10", ns1.Content)
	}
	ns2 := findRec(t, recs, "ns2", "A")
	if ns2.Content != "203.0.113.11" {
		t.Errorf("ns2 A content = %q, want 203.0.113.11", ns2.Content)
	}
}

func TestBootstrapRecords_NSARecord_OffZone_Skipped(t *testing.T) {
	srv := &models.ServerSettings{
		PublicIPv4: "203.0.113.10",
		NS1Name:    "ns1.other-zone.net",
		NS1IPv4:    "203.0.113.10",
	}
	recs := BootstrapRecords("zone-1", "example.com", srv, bootIDCounter(), true, true, true)
	for _, r := range recs {
		if r.Name == "ns1" && r.Type == "A" {
			t.Errorf("ns1 A leaked into zone where ns1_name lives elsewhere: %+v", r)
		}
	}
}

func TestBootstrapRecords_NSARecord_EmptyConfig_Noop(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.10"}
	recs := BootstrapRecords("zone-1", "example.com", srv, bootIDCounter(), true, true, true)
	for _, r := range recs {
		if (r.Name == "ns1" || r.Name == "ns2") && r.Type == "A" {
			t.Errorf("ns A record emitted without ns_name/ns_ipv4 config: %+v", r)
		}
	}
}

// includeMail=false (provider none/external) must omit ALL Jabali mail rows —
// no mail A/AAAA, no MX, no apex SPF, no _dmarc — while keeping apex + www +
// ns records (GH #189: a "No mail" domain never even briefly has mail DNS).
func TestBootstrapRecords_NoMailOmitsMailRows(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1", PublicIPv6: "2001:db8::1"}
	recs := BootstrapRecords("zone-1", "example.com", srv, bootIDCounter(), true, false, true)

	for _, r := range recs {
		switch {
		case r.Name == "mail" && (r.Type == "A" || r.Type == "AAAA"):
			t.Errorf("no-mail bootstrap must not add a mail %s record", r.Type)
		case r.Name == "@" && r.Type == "MX":
			t.Errorf("no-mail bootstrap must not add an MX record")
		case r.Name == "_dmarc":
			t.Errorf("no-mail bootstrap must not add a _dmarc record")
		case r.Name == "@" && r.Type == "TXT" && len(r.Content) >= 6 && r.Content[:6] == "\"v=spf":
			t.Errorf("no-mail bootstrap must not add an apex SPF record")
		}
	}
	// Apex A + www must still be present.
	if findRecOpt(recs, "@", "A") == nil {
		t.Errorf("apex A record should still be bootstrapped")
	}
	if findRecOpt(recs, "www", "CNAME") == nil {
		t.Errorf("www CNAME should still be bootstrapped")
	}
}

// GH #1449: includeApex=false (a web-off DNS-only / mail-only domain) must
// omit the apex A/AAAA pointing at this box — the tenant's web lives
// elsewhere — while still emitting the mail rows when includeMail=true.
func TestBootstrapRecords_NoApexOmitsApexAddr(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1", PublicIPv6: "2001:db8::1"}
	recs := BootstrapRecords("zone-1", "example.com", srv, bootIDCounter(), false, true, false)

	if findRecOpt(recs, "@", "A") != nil {
		t.Error("web-off bootstrap must not seed an apex A pointing at this box")
	}
	if findRecOpt(recs, "@", "AAAA") != nil {
		t.Error("web-off bootstrap must not seed an apex AAAA pointing at this box")
	}
	// Mail rows still present (mail-only domain).
	if findRecOpt(recs, "mail", "A") == nil {
		t.Error("mail A should still be bootstrapped for a mail-only domain")
	}
	if findRecOpt(recs, "@", "MX") == nil {
		t.Error("MX should still be bootstrapped for a mail-only domain")
	}
}

func findRecOpt(recs []models.DNSRecord, name, typ string) *models.DNSRecord {
	for i := range recs {
		if recs[i].Name == name && recs[i].Type == typ {
			return &recs[i]
		}
	}
	return nil
}

func TestBootstrapRecords_WWWOptOut(t *testing.T) {
	// GH #225: includeWWW=false must drop the www CNAME while keeping the
	// rest of the bootstrap set (apex A, mail rows, etc.).
	recs := BootstrapRecords(
		"zone1",
		"example.com",
		&models.ServerSettings{PublicIPv4: "192.0.2.1"},
		bootIDCounter(), true,
		true,
		false,
	)
	for _, r := range recs {
		if r.Name == "www" {
			t.Fatalf("www record must not be emitted when includeWWW=false; got %+v", r)
		}
	}
	// Sanity: the apex A still made it in.
	var haveApex bool
	for _, r := range recs {
		if r.Name == "@" && r.Type == "A" {
			haveApex = true
		}
	}
	if !haveApex {
		t.Fatal("apex A must still be emitted when includeWWW=false")
	}
}
