package userops

import "testing"

// The rename's WordPress URL step must accept what the agent's refresh verb
// accepts, a Plesk import's slug user (example_co_il) included, or it skips
// the rewrite with a warning for no reason.
func TestRefreshableOSUserRE(t *testing.T) {
	for _, ok := range []string{"alice", "example_co_il", "shop-1", "a"} {
		if !refreshableOSUserRE.MatchString(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "_lead", "Alice", "a.b", "a/b", "a b", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if refreshableOSUserRE.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
