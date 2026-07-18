package hestiacp

import "testing"

// GH #327: Hestia v-* helpers live in /usr/local/hestia/bin, which a
// non-interactive SSH session's PATH omits on Ubuntu → exit 127. Every remote
// command must be prefixed so they resolve.
func TestWithHestiaPath(t *testing.T) {
	got := withHestiaPath("v-list-users json")
	want := `export PATH="/usr/local/hestia/bin:$PATH"; v-list-users json`
	if got != want {
		t.Errorf("withHestiaPath = %q, want %q", got, want)
	}
	// export …; form so a piped/compound command is fully covered.
	comp := withHestiaPath("find /backup -newer /tmp/x | head -1")
	if comp[:len(`export PATH="/usr/local/hestia/bin:$PATH"; `)] != `export PATH="/usr/local/hestia/bin:$PATH"; ` {
		t.Errorf("compound command not prefixed: %q", comp)
	}
}
