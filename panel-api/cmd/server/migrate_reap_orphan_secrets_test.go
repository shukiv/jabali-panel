package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A job hard-deleted through the REST destroy path leaves its source
// credentials behind: the panel runs as the jabali user, the
// migration-secrets dir is root:jabali 0750, so the panel's own
// WipeJobSecret cannot unlink the file and the row is already gone. The
// root reaper must reclaim those orphans, while leaving a live job's secret,
// a too-young file (row not committed yet), and anything that is not a
// <job-id>.env secret alone.
func TestReapOrphanSecrets(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()

	mk := func(name string, mtime time.Time) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("SSH_PASSWORD=synthetic\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}

	orphanOld := mk("01KWJENDX2PK7ER3A880J261Z5.env", old)       // row-less + old   -> reap
	orphanYoung := mk("01KWJVJXDFKZ1R0TPN6QTDKS3W.env", fresh)   // row-less + young -> keep (in-flight guard)
	liveOld := mk("01KYJ8PK08HKKX2F2YWDH0PJBM.env", old)         // has a row + old  -> keep (row pass owns it)
	hostKey := mk("018KY2DB8XHNR50XVAVQ9QFXT2.known_hosts", old) // not a secret   -> keep
	notULID := mk("operator-notes.env", old)                     // not a job id   -> keep

	// A directory named like a secret is never removed.
	dirLike := filepath.Join(root, "01KYJ8S3Q6VPYMZDDNJM9QWAJ8.env")
	if err := os.Mkdir(dirLike, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dirLike, old, old); err != nil {
		t.Fatal(err)
	}

	rowExists := func(id string) (bool, error) { return id == "01KYJ8PK08HKKX2F2YWDH0PJBM", nil }

	n := reapOrphanSecrets(root, rowExists, time.Hour, false, io.Discard, io.Discard)
	if n != 1 {
		t.Fatalf("expected 1 secret reaped, got %d", n)
	}
	if _, err := os.Stat(orphanOld); !os.IsNotExist(err) {
		t.Errorf("old orphan secret should be removed, stat err=%v", err)
	}
	for name, p := range map[string]string{
		"young orphan": orphanYoung, "live job secret": liveOld,
		"known_hosts": hostKey, "non-job file": notULID, "directory": dirLike,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must be kept: %v", name, err)
		}
	}
}

// Dry-run counts but never removes.
func TestReapOrphanSecrets_DryRun(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "01KWJENDX2PK7ER3A880J261Z5.env")
	if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	if n := reapOrphanSecrets(root, noRow, time.Hour, true, io.Discard, io.Discard); n != 1 {
		t.Fatalf("dry-run should report 1, got %d", n)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("dry-run must not remove the secret: %v", err)
	}
}

// A missing secrets dir is a no-op, not an error.
func TestReapOrphanSecrets_MissingRoot(t *testing.T) {
	if n := reapOrphanSecrets(filepath.Join(t.TempDir(), "does-not-exist"), noRow, time.Hour, false, io.Discard, io.Discard); n != 0 {
		t.Fatalf("missing root should reap 0, got %d", n)
	}
}

// A row lookup that fails keeps the secret: without proof the job is gone,
// deleting could strand a live migration mid-run.
func TestReapOrphanSecrets_LookupErrorKeeps(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "01KWJENDX2PK7ER3A880J261Z5.env")
	if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	lookupFails := func(string) (bool, error) { return false, errors.New("db: connection refused") }
	if n := reapOrphanSecrets(root, lookupFails, time.Hour, false, io.Discard, io.Discard); n != 0 {
		t.Fatalf("lookup error must reap 0, got %d", n)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("secret must be kept when the row lookup fails: %v", err)
	}
}

func noRow(string) (bool, error) { return false, nil }
