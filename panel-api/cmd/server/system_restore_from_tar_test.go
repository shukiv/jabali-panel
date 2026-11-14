package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageSysRestoreContainer_CopiesIntoARootOnlyFolder(t *testing.T) {
	src := filepath.Join(t.TempDir(), "full.tar")
	if err := os.WriteFile(src, []byte("container"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "restore-staging")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	got, err := stageSysRestoreContainerIn(dir, uint32(os.Getuid()), src)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if filepath.Dir(got) != dir || !strings.HasPrefix(filepath.Base(got), "sysrestore-") || !strings.HasSuffix(got, ".tar") {
		t.Fatalf("copy at %s, want sysrestore-*.tar in %s", got, dir)
	}
	b, err := os.ReadFile(got)
	if err != nil || string(b) != "container" {
		t.Fatalf("copy = %q, %v", b, err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, got: 0o600} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: mode %v (%v), want %o", p, fi.Mode().Perm(), err, want)
		}
	}
}

func TestStageSysRestoreContainer_RefusesAFolderNotRoots(t *testing.T) {
	src := filepath.Join(t.TempDir(), "full.tar")
	if err := os.WriteFile(src, []byte("container"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "restore-staging")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	if _, err := stageSysRestoreContainerIn(link, uint32(os.Getuid()), src); err == nil {
		t.Fatal("a link in place of restore-staging must not be followed")
	}
	if _, err := stageSysRestoreContainerIn(victim, uint32(os.Getuid())+1, src); err == nil {
		t.Fatal("a folder root doesn't own must be refused")
	}
	if left, _ := os.ReadDir(victim); len(left) != 0 {
		t.Fatalf("nothing may be written there; found %v", left)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o755 {
		t.Fatalf("victim mode changed to %o", fi.Mode().Perm())
	}
}
