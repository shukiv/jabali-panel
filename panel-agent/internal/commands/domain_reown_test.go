package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// TestDomainReown_ValidatesInput covers the guards that reject before any
// filesystem mutation; the move + re-own path is covered by the .86 drill.
func TestDomainReown_ValidatesInput(t *testing.T) {
	cases := []struct {
		name      string
		old, newp string
		uid       int
	}{
		{"old not under /home", "/etc/passwd", "/home/bob/d/public_html", 1002},
		{"new not under /home", "/home/alice/d/public_html", "/tmp/x", 1002},
		{"traversal in old", "/home/alice/../../etc", "/home/bob/d/public_html", 1002},
		{"non-positive uid", "/home/alice/d/public_html", "/home/bob/d/public_html", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(domainReownParams{OldDocRoot: tc.old, NewDocRoot: tc.newp, NewUID: tc.uid})
			if _, err := domainReownHandler(context.Background(), raw); err == nil {
				t.Errorf("expected a validation error for old=%q new=%q uid=%d", tc.old, tc.newp, tc.uid)
			}
		})
	}
}

// TestShouldPruneRenameDir covers the GH #1579 empty-old-wrapper prune guard:
// it must fire only for a clean, /home-rooted STRICT ancestor of the old
// docroot, so it can never remove the docroot itself or an unrelated path.
func TestShouldPruneRenameDir(t *testing.T) {
	cases := []struct {
		name, dir, oldDocRoot string
		want                  bool
	}{
		{"nested wrapper", "/home/u/domains/old.com", "/home/u/domains/old.com/public_html", true},
		{"empty dir (default layout)", "", "/home/u/public_html/old.com", false},
		{"dir equals docroot", "/home/u/domains/old.com", "/home/u/domains/old.com", false},
		{"not under /home", "/srv/www/old.com", "/srv/www/old.com/public_html", false},
		{"not clean", "/home/u/domains/old.com/..", "/home/u/domains/old.com/public_html", false},
		{"sideways (not ancestor)", "/home/u/domains/other", "/home/u/domains/old.com/public_html", false},
		{"prefix but not path-boundary", "/home/u/domains/old", "/home/u/domains/old.com/public_html", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPruneRenameDir(tc.dir, tc.oldDocRoot); got != tc.want {
				t.Fatalf("shouldPruneRenameDir(%q, %q) = %v, want %v", tc.dir, tc.oldDocRoot, got, tc.want)
			}
		})
	}
}
