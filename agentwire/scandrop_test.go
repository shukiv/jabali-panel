package agentwire

import "testing"

func TestScanDropDir(t *testing.T) {
	got, err := ScanDropDir("/home/alice", "example.com", "scans")
	if err != nil || got != "/home/alice/domains/example.com/scans/scans" {
		t.Fatalf("ScanDropDir = %q, %v", got, err)
	}
	for _, c := range []struct{ domain, name string }{
		{"example.com", ""},
		{"example.com", ".."},
		{"example.com", ".hidden"},
		{"example.com", "a/b"},
		{"example.com", "Upper"},
		{"localhost", "scans"},
		{"../etc", "scans"},
		{"example.com/..", "scans"},
	} {
		if _, err := ScanDropDir("/home/alice", c.domain, c.name); err == nil {
			t.Errorf("ScanDropDir(%q, %q) accepted", c.domain, c.name)
		}
	}
}

func TestValidScanDropDir(t *testing.T) {
	d, n, err := ValidScanDropDir("/home/alice", "/home/alice/domains/xn--bcher-kva.example/scans/front-desk")
	if err != nil || d != "xn--bcher-kva.example" || n != "front-desk" {
		t.Fatalf("ValidScanDropDir = %q %q %v", d, n, err)
	}
	for _, dir := range []string{
		"/home/alice/domains/example.com/scans",
		"/home/alice/domains/example.com/scans/a/b",
		"/home/alice/domains/example.com/public_html/a",
		"/home/alice/domains/example.com/scans/..",
		"/home/alice/domains/example.com/scans/../../../../etc",
		"/home/alice/domains/../domains/example.com/scans/a",
		"/home/bob/domains/example.com/scans/a",
		"/home/alicex/domains/example.com/scans/a",
		"/home/alice/domains/example.com/scans/a/",
		"",
	} {
		if _, _, err := ValidScanDropDir("/home/alice", dir); err == nil {
			t.Errorf("ValidScanDropDir(%q) accepted", dir)
		}
	}
	if _, _, err := ValidScanDropDir("", "/domains/example.com/scans/a"); err == nil {
		t.Error("an empty home accepted")
	}
}
