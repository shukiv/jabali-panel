package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// JAB-155: orphan staging sweep. A staging dir whose migration_jobs row was
// hard-deleted must be reclaimed, but a live job's dir and a too-young dir
// must be left alone.
func TestReapOrphanStaging(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-30 * 24 * time.Hour) // well past maxAge
	fresh := time.Now()                         // just created

	// mk makes a staging dir with a file inside and sets its mtime.
	mk := func(name string, mtime time.Time) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(p, "extracted"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "user.tar.gz"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}

	orphanOld := mk("01ORPHANOLD0000000000000000", old)     // row-less + old  -> reap
	orphanYoung := mk("01ORPHANYOUNG00000000000000", fresh) // row-less + young -> keep (in-flight guard)
	liveOld := mk("01LIVEJOB000000000000000000", old)       // has a row + old  -> keep (row pass owns it)

	live := map[string]struct{}{"01LIVEJOB000000000000000000": {}}

	// Also drop a stray file (not a dir) to prove it is ignored.
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}

	n := reapOrphanStaging(root, live, 7*24*time.Hour, false, io.Discard, io.Discard)
	if n != 1 {
		t.Fatalf("expected 1 dir reaped, got %d", n)
	}
	if _, err := os.Stat(orphanOld); !os.IsNotExist(err) {
		t.Errorf("old orphan should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(orphanYoung); err != nil {
		t.Errorf("young orphan must be kept: %v", err)
	}
	if _, err := os.Stat(liveOld); err != nil {
		t.Errorf("live job dir must be kept: %v", err)
	}
}

// Dry-run counts but never removes.
func TestReapOrphanStaging_DryRun(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "01ORPHAN00000000000000000000")
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	n := reapOrphanStaging(root, nil, 7*24*time.Hour, true, io.Discard, io.Discard)
	if n != 1 {
		t.Fatalf("dry-run should report 1, got %d", n)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("dry-run must not remove the dir: %v", err)
	}
}

// A missing staging root is a no-op, not an error.
func TestReapOrphanStaging_MissingRoot(t *testing.T) {
	if n := reapOrphanStaging(filepath.Join(t.TempDir(), "does-not-exist"), nil, time.Hour, false, io.Discard, io.Discard); n != 0 {
		t.Fatalf("missing root should reap 0, got %d", n)
	}
}
