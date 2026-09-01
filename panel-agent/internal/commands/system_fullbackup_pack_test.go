package commands

import "testing"

// GH #1408: a pack label becomes a path under the stage dir (system.tar.zst or
// users/<username>.tar.zst), so it must be a strict allowlist — a traversal or
// odd label could write outside the container staging.
func TestFullpkgLabelRE(t *testing.T) {
	good := []string{"system", "users/alice", "users/a", "users/a_b-c", "users/user01"}
	bad := []string{
		"", "system/x", "users", "users/", "users/../etc", "../system",
		"users/UPPER", "users/1abc", "users/a/b", "users/a;b", "users//x",
		"System", "sys tem",
	}
	for _, g := range good {
		if !fullpkgLabelRE.MatchString(g) {
			t.Fatalf("label %q should be valid", g)
		}
	}
	for _, b := range bad {
		if fullpkgLabelRE.MatchString(b) {
			t.Fatalf("label %q should be rejected", b)
		}
	}
}
