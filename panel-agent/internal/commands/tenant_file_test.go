package commands

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// useTenantHomeRoot points tenantHomeRoot at a temporary tree for one test and
// returns the account's home directory inside it.
func useTenantHomeRoot(t *testing.T, account string) string {
	t.Helper()
	root := t.TempDir()
	prev := tenantHomeRoot
	tenantHomeRoot = root
	t.Cleanup(func() { tenantHomeRoot = prev })
	home := filepath.Join(root, account)
	if err := os.MkdirAll(filepath.Join(home, "domains", "example.com", "public_html"), 0o750); err != nil {
		t.Fatal(err)
	}
	return home
}

const wpVersionLine = "<?php\n$wp_version = '6.5.2';\n"

// A tenant-owned wp-includes that is a symlink to a directory outside the home
// must not be read as root: the version file it points at is not the site's.
func TestReadWPVersion_RefusesSymlinkedIncludesOutsideHome(t *testing.T) {
	home := useTenantHomeRoot(t, "tu")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "version.php"), []byte(wpVersionLine), 0o644); err != nil {
		t.Fatal(err)
	}
	docroot := filepath.Join(home, "domains", "example.com", "public_html")
	if err := os.Symlink(outside, filepath.Join(docroot, "wp-includes")); err != nil {
		t.Fatal(err)
	}
	if got := readWPVersion(docroot); got != "" {
		t.Fatalf("readWPVersion followed a link out of the home: got %q", got)
	}
}

// The reader must not hang on a FIFO planted where version.php should be.
func TestReadTenantRegular_RefusesFIFOWithoutBlocking(t *testing.T) {
	home := useTenantHomeRoot(t, "tu")
	p := filepath.Join(home, "domains", "example.com", "public_html", "fifo.php")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTenantRegular(p); err == nil {
		t.Fatal("readTenantRegular accepted a FIFO")
	}
}

// A regular file over the cap is refused, not read in full.
func TestReadTenantRegular_CapsSize(t *testing.T) {
	home := useTenantHomeRoot(t, "tu")
	p := filepath.Join(home, "domains", "example.com", "public_html", "big.php")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", tenantReadMax+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTenantRegular(p); err == nil {
		t.Fatal("readTenantRegular accepted a file over tenantReadMax")
	}
}

// A symlink planted at the leaf of a file the installer writes must not be
// followed: the outside target stays untouched.
func TestWriteTenantFile_RefusesPlantedLeafSymlink(t *testing.T) {
	home := useTenantHomeRoot(t, "tu")
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "domains", "example.com", "public_html", "wp-config.php")
	if err := os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	if err := writeTenantFile(p, []byte("pwned"), 0o640, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("writeTenantFile wrote through a planted symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatalf("outside file was modified: %q", b)
	}
}

// Writes and reads still work for a normal file inside the tenant tree.
func TestTenantFile_RoundTripInsideHome(t *testing.T) {
	home := useTenantHomeRoot(t, "tu")
	p := filepath.Join(home, "domains", "example.com", "public_html", "composer.json")
	if err := writeTenantFile(p, []byte("{}"), 0o644, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := readTenantRegular(p)
	if err != nil || string(b) != "{}" {
		t.Fatalf("read back %q, %v", b, err)
	}
}

// A subdirectory that climbs out of the docroot must not produce an install
// path, for the Drupal install and for the delete that runs rm -rf on it.
func TestComputeDrupalInstallPath_RefusesTraversal(t *testing.T) {
	const docroot = "/home/u/domains/example.com/public_html"
	if p, err := computeDrupalInstallPath(docroot, "../../../../etc"); err == nil {
		t.Fatalf("traversal subdirectory accepted: %q", p)
	}
	if p, err := computeDrupalInstallPath(docroot, "blog"); err != nil || p != docroot+"/blog" {
		t.Fatalf("plain subdirectory: got %q, %v", p, err)
	}
}

// A symlink standing where a destination component should be is refused, and
// nothing behind it is created or chowned. This is a regression guard, not a
// proof of the swap window: that window is closed by creating each component
// through its parent descriptor (fsperm.OpenDirUnder).
func TestEnsureOwnedSetgidDir_RefusesSymlinkComponent(t *testing.T) {
	anchor := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(anchor, "domains")); err != nil {
		t.Fatal(err)
	}
	err := ensureOwnedSetgidDir(anchor, filepath.Join(anchor, "domains", "example.com"), os.Getuid(), os.Getgid())
	if err == nil {
		t.Fatal("ensureOwnedSetgidDir followed a symlink component")
	}
	if _, serr := os.Lstat(filepath.Join(outside, "example.com")); serr == nil {
		t.Fatal("a directory was created behind the symlink")
	}
}
