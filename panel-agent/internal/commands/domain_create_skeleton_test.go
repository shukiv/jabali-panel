package commands

import (
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// GH #465 reopen: domain.create re-runs every reconcile tick, and the skeleton
// used to be re-laid unconditionally — a user-deleted skeleton file reappeared
// ~30s later. The skeleton must be written only by the call that created the
// docroot.
func TestLayAccountSkeleton_OnlyOnFreshDocroot(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	docRoot := filepath.Join(home, "domains", "example.com", "public_html")
	scope := filesafe.NewScopeForTest("u", "alice", home)
	if _, err := scope.MkdirInScope(docRoot, true, 0o755); err != nil {
		t.Fatalf("mkdir docroot: %v", err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	files := []skeletonFileParam{
		{RelPath: "welcome.html", Content: []byte("<h1>hi</h1>")},
		{RelPath: "assets/site.css", Content: []byte("body{}")},
	}

	// Create tick: docroot was just created — skeleton lands.
	if warns := layAccountSkeleton(scope, true, docRoot, "alice", uid, gid, files); len(warns) != 0 {
		t.Fatalf("unexpected warnings on fresh docroot: %v", warns)
	}
	got, err := os.ReadFile(filepath.Join(docRoot, "welcome.html"))
	if err != nil {
		t.Fatalf("skeleton file not written: %v", err)
	}
	if string(got) != "<h1>hi</h1>" {
		t.Errorf("skeleton content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(docRoot, "assets", "site.css")); err != nil {
		t.Errorf("nested skeleton file not written: %v", err)
	}

	// User deletes a skeleton file, then the reconciler re-runs domain.create
	// (docroot already exists) — the file must STAY deleted.
	if err := os.Remove(filepath.Join(docRoot, "welcome.html")); err != nil {
		t.Fatal(err)
	}
	if warns := layAccountSkeleton(scope, false, docRoot, "alice", uid, gid, files); warns != nil {
		t.Fatalf("reconcile pass wrote skeleton: %v", warns)
	}
	if _, err := os.Stat(filepath.Join(docRoot, "welcome.html")); !os.IsNotExist(err) {
		t.Error("deleted skeleton file was resurrected by a reconcile pass")
	}
}

// A fresh-create pass must never clobber a file that already exists at a
// skeleton path — writeSkeleton silently skips it (Lstat pre-check, O_EXCL
// backstop).
func TestLayAccountSkeleton_NeverClobbersExisting(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	docRoot := filepath.Join(home, "public_html")
	scope := filesafe.NewScopeForTest("u", "alice", home)
	if _, err := scope.MkdirInScope(docRoot, true, 0o755); err != nil {
		t.Fatalf("mkdir docroot: %v", err)
	}
	existing := filepath.Join(docRoot, "index.html")
	if err := os.WriteFile(existing, []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []skeletonFileParam{{RelPath: "index.html", Content: []byte("skeleton")}}

	warns := layAccountSkeleton(scope, true, docRoot, "alice", os.Getuid(), os.Getgid(), files)
	if len(warns) != 0 {
		t.Fatalf("existing path should be skipped silently, got %v", warns)
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user content" {
		t.Errorf("existing file clobbered: %q", got)
	}
}
