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
