package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveMailVhostFiles covers domain.delete's mail-vhost reap: it removes
// `<domain>-mail.conf` from BOTH sites-available and sites-enabled, is
// idempotent, and never touches an unrelated domain's mail vhost.
//
// Regression: `jabali user delete` (and the HTTP delete via
// Reconciler.ReconcileDeleted) orphaned the per-domain mail vhost, because
// reconcileWebmailVhosts only visits domains that still exist — a deleted
// domain's mail vhost was never reaped. domain.delete is the single teardown
// chokepoint both paths route through, so the reap belongs here.
func TestRemoveMailVhostFiles(t *testing.T) {
	avail, enabl := wireMailVhostPaths(t)

	const dom = "delete-me.test"
	confA := filepath.Join(avail, dom+"-mail.conf")
	confE := filepath.Join(enabl, dom+"-mail.conf")
	// An unrelated domain whose mail vhost must survive the reap.
	const keep = "keep.test"
	keepA := filepath.Join(avail, keep+"-mail.conf")

	for _, p := range []string{confA, confE, keepA} {
		if err := os.WriteFile(p, []byte("server {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// First call removes both target files and reports changed=true.
	if changed := removeMailVhostFiles(dom); !changed {
		t.Fatalf("removeMailVhostFiles(%q) = false, want true (files existed)", dom)
	}
	for _, p := range []string{confA, confE} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("mail vhost %s still present after reap (err=%v)", p, err)
		}
	}
	// The unrelated domain's mail vhost must be untouched.
	if _, err := os.Stat(keepA); err != nil {
		t.Errorf("unrelated mail vhost %s was removed: %v", keepA, err)
	}

	// Idempotent: a second call finds nothing and reports changed=false.
	if changed := removeMailVhostFiles(dom); changed {
		t.Errorf("removeMailVhostFiles(%q) second call = true, want false (already gone)", dom)
	}
}
