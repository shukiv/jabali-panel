package dnscompile

import (
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1459: NormalizeName must leave every ASCII name byte-identical (so
// healthy zones compile unchanged — no push-hash / zone-cache churn) and
// punycode only genuine IDN labels, while rejecting names PowerDNS's
// latin1 columns cannot store (which the caller then skips instead of
// rolling back the whole atomic zone upsert).
func TestNormalizeName(t *testing.T) {
	longLabel := strings.Repeat("a", 64)                 // 64 > 63 octet label limit
	long253 := strings.Repeat("a.", 127) + "example.com" // > 253 octets total

	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		// Identity for ordinary ASCII — the 99.9% path. Case, underscores,
		// wildcard and hyphens all preserved verbatim.
		{"plain", "www.example.com", "www.example.com", true},
		{"apex", "example.com", "example.com", true},
		{"dmarc underscore", "_dmarc.example.com", "_dmarc.example.com", true},
		{"dkim underscore", "mail._domainkey.example.com", "mail._domainkey.example.com", true},
		{"acme underscore", "_acme-challenge.example.com", "_acme-challenge.example.com", true},
		{"srv underscores", "_25._tcp.example.com", "_25._tcp.example.com", true},
		{"wildcard", "*.example.com", "*.example.com", true},
		{"mixed case preserved", "WWW.Example.COM", "WWW.Example.COM", true},
		{"hyphens", "my-host-1.example.com", "my-host-1.example.com", true},
		{"trailing dot stripped", "www.example.com.", "www.example.com", true},

		// Reject the storage-unsafe class → caller skips the record.
		{"empty", "", "", false},
		{"dot only", ".", "", false},
		{"empty label", "a..b.example.com", "", false},
		{"label too long", longLabel + ".example.com", "", false},
		{"name too long", long253, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeName(tc.in)
			if ok != tc.ok {
				t.Fatalf("NormalizeName(%q) ok=%v, want %v (got %q)", tc.in, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("NormalizeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeName_IDNPunycode(t *testing.T) {
	// München — the canonical IDN example. Its punycode form is stable.
	got, ok := NormalizeName("münchen.example.com")
	if !ok {
		t.Fatal("münchen.example.com should normalize, got !ok")
	}
	if got != "xn--mnchen-3ya.example.com" {
		t.Errorf("münchen punycode = %q, want xn--mnchen-3ya.example.com", got)
	}

	// A label mixing an underscore prefix with a non-ASCII rune still
	// encodes (lenient profile — no STD3 rejection of the underscore).
	got, ok = NormalizeName("_café.example.com")
	if !ok {
		t.Fatalf("_café.example.com should normalize, got !ok (%q)", got)
	}
	if !isASCIIStr(got) {
		t.Errorf("normalized name must be pure ASCII, got %q", got)
	}

	// Idempotent: re-normalizing the ASCII output is a no-op.
	again, ok := NormalizeName(got)
	if !ok || again != got {
		t.Errorf("NormalizeName not idempotent: %q -> %q", got, again)
	}
}

func mkZone() *models.DNSZone {
	return &models.DNSZone{
		ID: "zone1", DomainID: "dom1", Name: "example.com",
		RefreshSeconds: 3600, RetrySeconds: 600, ExpireSeconds: 604800, MinimumTTL: 3600,
		IsEnabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

// A record whose name cannot be stored must be dropped, NOT allowed to
// fail the whole zone — while every other record survives (GH #1459).
func TestCompile_SkipsUnstorableRecordName(t *testing.T) {
	zone := mkZone()
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1", NS1Name: "ns1.example.com"}
	bad := strings.Repeat("a", 64) // over-long label -> unstorable
	recs := []models.DNSRecord{
		{ID: "r1", ZoneID: zone.ID, Name: "good", Type: "A", Content: "192.0.2.9", TTL: 3600, IsEnabled: true},
		{ID: "r2", ZoneID: zone.ID, Name: bad, Type: "A", Content: "192.0.2.10", TTL: 3600, IsEnabled: true},
	}
	out := Compile(zone, recs, srv)

	var sawGood, sawBad bool
	for _, r := range out {
		if r.Name == "good.example.com" && r.Type == "A" {
			sawGood = true
		}
		if strings.Contains(r.Name, bad) {
			sawBad = true
		}
	}
	if !sawGood {
		t.Error("the storable record was dropped")
	}
	if sawBad {
		t.Error("the unstorable record was NOT skipped — it would roll back the whole zone")
	}
}

// An IDN record name is punycode-encoded (latin1-safe) in the compiled
// output; the apex of an IDN zone is punycoded too.
func TestCompile_PunycodesIDN(t *testing.T) {
	zone := mkZone()
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1", NS1Name: "ns1.example.com"}
	recs := []models.DNSRecord{
		{ID: "r1", ZoneID: zone.ID, Name: "café", Type: "A", Content: "192.0.2.9", TTL: 3600, IsEnabled: true},
	}
	out := Compile(zone, recs, srv)

	var found bool
	for _, r := range out {
		if r.Type == "A" && r.Content == "192.0.2.9" {
			found = true
			if !strings.HasPrefix(r.Name, "xn--") || !strings.HasSuffix(r.Name, ".example.com") {
				t.Errorf("IDN name not punycoded: %q", r.Name)
			}
			if !isASCIIStr(r.Name) {
				t.Errorf("compiled name must be ASCII, got %q", r.Name)
			}
		}
	}
	if !found {
		t.Fatal("IDN A record missing from compiled output")
	}
}

func TestCompile_IDNApex(t *testing.T) {
	zone := mkZone()
	zone.Name = "münchen.example" // raw-IDN apex
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1", NS1Name: "ns1.example.com"}
	out := Compile(zone, nil, srv)

	for _, r := range out {
		if r.Type == "SOA" {
			if !strings.HasPrefix(r.Name, "xn--mnchen-3ya.example") {
				t.Errorf("SOA apex not punycoded: %q", r.Name)
			}
			return
		}
	}
	t.Fatal("no SOA in output")
}

// The compiled names for a plain ASCII zone must be byte-identical to the
// pre-#1459 output, so already-provisioned zones never re-push.
func TestCompile_ASCIIIdentityNoChurn(t *testing.T) {
	zone := mkZone()
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1", NS1Name: "ns1.example.com"}
	recs := []models.DNSRecord{
		{ID: "r1", ZoneID: zone.ID, Name: "_dmarc", Type: "TXT", Content: "v=DMARC1; p=none", TTL: 3600, IsEnabled: true},
		{ID: "r2", ZoneID: zone.ID, Name: "*", Type: "A", Content: "192.0.2.9", TTL: 3600, IsEnabled: true},
	}
	out := Compile(zone, recs, srv)

	want := map[string]bool{"_dmarc.example.com": false, "*.example.com": false}
	for _, r := range out {
		if _, ok := want[r.Name]; ok {
			want[r.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected compiled name %q verbatim (churn/mutation)", name)
		}
	}
}
