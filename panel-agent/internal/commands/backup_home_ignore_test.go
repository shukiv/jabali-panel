package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHomeBackupIgnoreFile guards GH #454: the per-account ~/.backupignore is
// honoured only when it's a real regular file. It is user-writable and the
// backup agent reads it as root, so a symlink, directory, or oversized file
// must be rejected (defence in depth) and an absent file is a no-op.
func TestHomeBackupIgnoreFile(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, BackupIgnoreFile)

	// absent -> ("", false)
	if got, ok := homeBackupIgnoreFile(home); ok {
		t.Errorf("absent .backupignore: got (%q,%v), want (\"\",false)", got, ok)
	}

	// regular file -> (path, true)
	if err := os.WriteFile(p, []byte("cache/\nnode_modules/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := homeBackupIgnoreFile(home); !ok || got != p {
		t.Errorf("regular .backupignore: got (%q,%v), want (%q,true)", got, ok, p)
	}

	// symlink -> rejected (must not let a user redirect the root read)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "real.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if _, ok := homeBackupIgnoreFile(home); ok {
		t.Error("symlink .backupignore must be rejected (GH #454 security)")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	// oversized -> rejected
	if err := os.WriteFile(p, make([]byte, 256*1024+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := homeBackupIgnoreFile(home); ok {
		t.Error("oversized .backupignore must be rejected")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	// directory -> rejected
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := homeBackupIgnoreFile(home); ok {
		t.Error("directory .backupignore must be rejected")
	}
}
