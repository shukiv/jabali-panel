package cpanel

import (
	"os"
	"path/filepath"
	"testing"
)

// JAB-249: a migrated domain must get CreateWWW=1 exactly when the source
// zone publishes a www.<domain> address record, so the issued LE cert covers
// www (the #1069 SAN-drift reconciler expands it). A source without www stays
// apex-only (unchanged default).
func TestZoneHasWWWRecord(t *testing.T) {
	dir := t.TempDir()
	const soa = "$TTL 14400\n" +
		"@ IN SOA ns1.example.com. admin.example.com. ( 1 2 3 4 5 )\n" +
		"@ IN NS ns1.example.com.\n" +
		"@ IN A 192.0.2.1\n"

	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	wwwCNAME := write("example.com.db", soa+"www IN CNAME example.com.\n")
	wwwA := write("a.example.com.db", soa+"www IN A 192.0.2.9\n")
	wwwFQDN := write("f.example.com.db", soa+"www.f.example.com. IN A 192.0.2.9\n")
	wwwUpper := write("u.example.com.db", soa+"WWW IN A 192.0.2.9\n")
	noWWW := write("no.example.com.db", soa+"mail IN A 192.0.2.2\n")

	cases := []struct {
		name   string
		path   string
		domain string
		want   bool
	}{
		{"www cname (cpanel default)", wwwCNAME, "example.com", true},
		{"www a record", wwwA, "a.example.com", true},
		{"www as fqdn owner", wwwFQDN, "f.example.com", true},
		{"www case-insensitive", wwwUpper, "u.example.com", true},
		{"no www record stays apex-only", noWWW, "no.example.com", false},
		{"missing zone file is conservative false", filepath.Join(dir, "nope.db"), "x.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := zoneHasWWWRecord(tc.path, tc.domain); got != tc.want {
				t.Fatalf("zoneHasWWWRecord(%q) = %v, want %v", tc.domain, got, tc.want)
			}
		})
	}
}
