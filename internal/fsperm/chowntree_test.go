package fsperm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The ChownTree tests chown to one of the test user's supplementary groups,
// which needs no root, so a chown that lands where it shouldn't is visible
// as a changed group.

func otherGroup(t *testing.T) int {
	t.Helper()
	gids, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gids {
		if g != os.Getgid() {
			return g
		}
	}
	t.Skip("the test user has no supplementary group to chown to")
	return -1
}

func gidOf(t *testing.T, path string) int {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Sys().(*syscall.Stat_t).Gid)
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
}

// outsideTree is a directory outside the home with a file in it: what a
// tenant's symlink would point root's chown at.
func outsideTree(t *testing.T) (dir, file string) {
	dir = filepath.Join(t.TempDir(), "secret")
	file = filepath.Join(dir, "passwd")
	write(t, file)
	return dir, file
}

func TestChownTree_ChownsTheTreeWithoutFollowingLinks(t *testing.T) {
	gid := otherGroup(t)
	home := t.TempDir()
	file := filepath.Join(home, "domains", "a.test", "public_html", "index.php")
	write(t, file)
	outDir, outFile := outsideTree(t)
	link := filepath.Join(home, "domains", "a.test", "evil")
	if err := os.Symlink(outDir, link); err != nil {
		t.Fatal(err)
	}

	if err := ChownTree(home, home, os.Getuid(), gid); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{home, filepath.Join(home, "domains"), filepath.Dir(file), file, link} {
		if got := gidOf(t, p); got != gid {
			t.Errorf("%s: gid %d, want %d", p, got, gid)
		}
	}
	for _, p := range []string{outDir, outFile} {
		if got := gidOf(t, p); got != os.Getgid() {
			t.Errorf("%s outside the home was chowned through a link (gid %d)", p, got)
		}
	}
}

func TestChownTree_RefusesALinkBetweenTheHomeAndRoot(t *testing.T) {
	gid := otherGroup(t)
	home := t.TempDir()
	outDir, outFile := outsideTree(t)
	write(t, filepath.Join(filepath.Dir(outDir), "a.test", "index.php"))
	// ~/domains is a link: root's chown of ~/domains/a.test must not follow it.
	if err := os.Symlink(filepath.Dir(outDir), filepath.Join(home, "domains")); err != nil {
		t.Fatal(err)
	}

	if err := ChownTree(home, filepath.Join(home, "domains", "a.test"), os.Getuid(), gid); err == nil {
		t.Error("a link between the home and root was followed")
	}
	for _, p := range []string{filepath.Join(filepath.Dir(outDir), "a.test", "index.php"), outFile} {
		if got := gidOf(t, p); got != os.Getgid() {
			t.Errorf("%s outside the home was chowned (gid %d)", p, got)
		}
	}
}

func TestChownTree_DirectorySwappedForALinkMidWalk(t *testing.T) {
	gid := otherGroup(t)
	home := t.TempDir()
	sub := filepath.Join(home, "uploads")
	write(t, filepath.Join(sub, "img.jpg"))
	outDir, outFile := outsideTree(t)
	// The tenant swaps ~/uploads for a link after the walk saw a directory.
	beforeDescend = func(_ int, name string) {
		if name != "uploads" {
			return
		}
		if err := os.Rename(sub, sub+".old"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(outDir, sub); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeDescend = nil })

	if err := ChownTree(home, home, os.Getuid(), gid); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{outDir, outFile} {
		if got := gidOf(t, p); got != os.Getgid() {
			t.Errorf("%s outside the home was chowned through the swapped link (gid %d)", p, got)
		}
	}
	if got := gidOf(t, sub); got != gid {
		t.Errorf("the swapped-in link itself: gid %d, want %d", got, gid)
	}
}

func TestChownTree_RootMustBeUnderTheAnchor(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	if err := ChownTree(home, other, os.Getuid(), os.Getgid()); err == nil {
		t.Error("a root outside the anchor was accepted")
	}
}
