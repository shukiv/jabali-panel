package commands

import (
	"strings"
	"testing"
)

// JAB-201: the production blind spot — a maintenance plugin calling WP's
// nocache_headers() on every response. Its Expires: 1984 signature must
// win over the accompanying Cache-Control, because it names the CAUSE
// (a plugin) rather than the mechanism.
func TestClassifyCacheBlock_MaintenanceSignatureWins(t *testing.T) {
	hdr := strings.Join([]string{
		"HTTP/2 503",
		"Cache-Control: no-cache, must-revalidate, max-age=0",
		"Expires: Wed, 11 Jan 1984 05:00:00 GMT",
		"X-Jabali-Cache: BYPASS",
	}, "\r\n")
	cause, evidence := classifyCacheBlock(hdr)
	if cause != "wp_nocache_headers" {
		t.Fatalf("cause = %q, want wp_nocache_headers", cause)
	}
	if !strings.Contains(evidence, "1984") {
		t.Errorf("evidence should carry the header line, got %q", evidence)
	}
}

func TestClassifyCacheBlock_Table(t *testing.T) {
	cases := []struct {
		name, hdr, want string
	}{
		{"cache-control private", "Cache-Control: private, max-age=0", "upstream_cache_control"},
		{"no-store", "cache-control: no-store", "upstream_cache_control"},
		{"vary cookie", "Vary: Cookie", "vary_header"},
		{"vary accept-encoding is benign", "Vary: Accept-Encoding", ""},
		{"set-cookie", "Set-Cookie: PHPSESSID=abc; path=/", "response_sets_cookie"},
		{"clean response", "Cache-Control: max-age=600\r\nX-Jabali-Cache: MISS", ""},
	}
	for _, tc := range cases {
		if got, _ := classifyCacheBlock(tc.hdr); got != tc.want {
			t.Errorf("%s: cause = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTallyJcacheLog(t *testing.T) {
	log := "HIT\nMISS\nHIT\nBYPASS\n-\n\nEXPIRED\nMISS\n"
	hits, misses, bypass, other := tallyJcacheLog(strings.NewReader(log))
	if hits != 2 || misses != 2 || bypass != 1 || other != 1 {
		t.Errorf("got hits=%d misses=%d bypass=%d other=%d", hits, misses, bypass, other)
	}
}

func TestTallyJcacheLogFile_MissingIsNotZeroPercent(t *testing.T) {
	// A missing log means "no data", never "0% HIT" — the advisor must
	// not warn a fresh site into a panic.
	_, _, _, _, ok := tallyJcacheLogFile("no-such-host.example")
	if ok {
		t.Fatal("missing log must report ok=false")
	}
}
