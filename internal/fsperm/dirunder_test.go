package fsperm

import (
	"os"
	"path/filepath"
	"testing"
)

// OpenDirUnder (GH #2303): a restore writes a domain's files only into a
// folder reached from the account's home without following a link.
func TestOpenDirUnder(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	real := filepath.Join(home, "domains", "real.example")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "domains", "link.example")); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()

	f, err := OpenDirUnder(home, real, 0o750, uid, gid)
	if err != nil {
		t.Fatalf("real folder: %v", err)
	}
	if !sameFile(t, f, real) {
		t.Error("opened something other than the real folder")
	}
	f.Close()

	if f, err := OpenDirUnder(home, filepath.Join(home, "domains", "link.example"), 0o750, uid, gid); err == nil {
		f.Close()
		t.Fatal("a link in place of the folder was accepted")
	}
	if f, err := OpenDirUnder(home, elsewhere, 0o750, uid, gid); err == nil {
		f.Close()
		t.Error("a folder outside the anchor was accepted")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("something was created behind the link: %v", entries)
	}
}

// Missing folders on the way are created, with the mode and owner given.
func TestOpenDirUnder_CreatesMissing(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, "domains", "new.example")
	f, err := OpenDirUnder(home, want, 0o750, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !sameFile(t, f, want) {
		t.Fatal("opened something other than the new folder")
	}
	for _, p := range []string{filepath.Join(home, "domains"), want} {
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() {
			t.Fatalf("%s: not created as a folder (%v)", p, err)
		}
		if st.Mode().Perm() != 0o750 {
			t.Errorf("%s: mode %v, want 0750", p, st.Mode().Perm())
		}
	}
}

// A link higher up the path is refused the same way.
func TestOpenDirUnder_LinkInTheMiddle(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(home, "domains")); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenDirUnder(home, filepath.Join(home, "domains", "real.example"), 0o750, os.Getuid(), os.Getgid()); err == nil {
		f.Close()
		t.Fatal("link in the middle of the path was accepted")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("something was created behind the link: %v", entries)
	}
}

func sameFile(t *testing.T, f *os.File, path string) bool {
	t.Helper()
	a, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(a, b)
}
