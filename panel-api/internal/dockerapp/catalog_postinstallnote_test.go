package dockerapp

import "testing"

// TestPocketIDHasPostInstallNote pins the GH #521 fix: the Pocket ID catalog
// entry must carry a post-install note pointing operators at /setup (Pocket ID
// v2 creates the first admin there, not at /), with the {{domain}} token the
// API substitutes for the assigned domain.
func TestPocketIDHasPostInstallNote(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	e, ok := cat.Get("pocket-id")
	if !ok {
		t.Fatal("pocket-id entry not found in catalog")
	}
	if e.PostInstallNote == "" {
		t.Fatal("pocket-id has no post_install_note (GH #521 regression)")
	}
	for _, want := range []string{"/setup", "{{domain}}"} {
		if !contains(e.PostInstallNote, want) {
			t.Errorf("post_install_note missing %q; got: %q", want, e.PostInstallNote)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
