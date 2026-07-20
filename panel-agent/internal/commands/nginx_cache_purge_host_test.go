package commands

import "testing"

// Gitea #418: purge must match the host FIELD exactly, never a substring.
func TestKeyLineHost(t *testing.T) {
	cases := []struct{ line, want string }{
		{"KEY: httpsGETa.com/path", "a.com"},
		{"KEY: httpGETa.com/", "a.com"},
		{"KEY: httpsHEADsub.a.com/x", "sub.a.com"},         // must NOT be "a.com"
		{"KEY: httpsGETmya.com/", "mya.com"},               // must NOT be "a.com"
		{"KEY: httpsGETvictim.com/go/a.com", "victim.com"}, // path contains a.com
		{"KEY: httpsPOSTa.com/cart", "a.com"},
		// cache-query-allowlist: query-variant entries carry a "?paged=2&" suffix
		// after the path — whole-domain purge must still extract the host so those
		// variants are evicted alongside the base page.
		{"KEY: httpsGETa.com/blog?paged=2&", "a.com"},
	}
	for _, c := range cases {
		if got := keyLineHost([]byte(c.line)); got != c.want {
			t.Errorf("keyLineHost(%q) = %q, want %q", c.line, got, c.want)
		}
	}
	// the cross-tenant cases must not match a victim purge for "a.com"
	for _, l := range []string{"KEY: httpsHEADsub.a.com/x", "KEY: httpsGETmya.com/", "KEY: httpsGETvictim.com/go/a.com"} {
		if keyLineHost([]byte(l)) == "a.com" {
			t.Errorf("%q wrongly matches a.com (cross-tenant evict)", l)
		}
	}
}
