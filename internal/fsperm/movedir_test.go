package fsperm

import (
	"os"
	"path/filepath"
	"testing"
)

// moveTree lays out three homes under a temp dir: alice owns a.example, bob
// owns b.example (with a file in its docroot), and carol is the new owner of
// a.example with its wrapper folder already made.
func moveTree(t *testing.T) (home, alice, bob, carol string) {
	t.Helper()
	home = t.TempDir()
	alice, bob, carol = filepath.Join(home, "alice"), filepath.Join(home, "bob"), filepath.Join(home, "carol")
	for _, d := range []string{
		filepath.Join(alice, "domains", "a.example", "public_html"),
		filepath.Join(bob, "domains", "b.example", "public_html"),
		filepath.Join(carol, "domains", "a.example"),
	} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bob, "domains", "b.example", "public_html", "index.html"), []byte("bob"), 0o640); err != nil {
		t.Fatal(err)
	}
	return home, alice, bob, carol
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestMoveDirUnder(t *testing.T) {
	t.Run("moves the docroot", func(t *testing.T) {
		_, alice, _, carol := moveTree(t)
		src := filepath.Join(alice, "domains", "a.example", "public_html")
		dst := filepath.Join(carol, "domains", "a.example", "public_html")
		if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("alice"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := MoveDirUnder(alice, src, carol, dst); err != nil {
			t.Fatal(err)
		}
		if exists(src) || !exists(filepath.Join(dst, "index.html")) {
			t.Fatal("the docroot did not move with its files")
		}
	})

	// The old owner swaps their wrapper folder for a link to another tenant's
	// domain folder. A path-based rename follows it.
	linkWrapper := func(t *testing.T, alice, bob string) string {
		t.Helper()
		wrapper := filepath.Join(alice, "domains", "a.example")
		if err := os.RemoveAll(wrapper); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(bob, "domains", "b.example"), wrapper); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(wrapper, "public_html")
	}
	t.Run("a plain rename follows a link in the source path", func(t *testing.T) {
		_, alice, bob, carol := moveTree(t)
		src := linkWrapper(t, alice, bob)
		dst := filepath.Join(carol, "domains", "a.example", "public_html")
		if err := os.Rename(src, dst); err != nil {
			t.Fatal(err)
		}
		if !exists(filepath.Join(dst, "index.html")) {
			t.Fatal("expected os.Rename to move bob's docroot (the case MoveDirUnder refuses)")
		}
	})
	t.Run("a link in the source path is refused", func(t *testing.T) {
		_, alice, bob, carol := moveTree(t)
		src := linkWrapper(t, alice, bob)
		dst := filepath.Join(carol, "domains", "a.example", "public_html")
		if err := MoveDirUnder(alice, src, carol, dst); err == nil {
			t.Fatal("moved through a link")
		}
		if !exists(filepath.Join(bob, "domains", "b.example", "public_html", "index.html")) || exists(dst) {
			t.Fatal("bob's docroot moved")
		}
	})
	t.Run("a link as the docroot is refused", func(t *testing.T) {
		_, alice, bob, carol := moveTree(t)
		src := filepath.Join(alice, "domains", "a.example", "public_html")
		if err := os.Remove(src); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(bob, "domains", "b.example", "public_html"), src); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(carol, "domains", "a.example", "public_html")
		if err := MoveDirUnder(alice, src, carol, dst); err == nil {
			t.Fatal("moved a link")
		}
		if exists(dst) {
			t.Fatal("something landed at the destination")
		}
	})
	t.Run("a link in the destination path is refused", func(t *testing.T) {
		_, alice, bob, carol := moveTree(t)
		wrapper := filepath.Join(carol, "domains", "a.example")
		if err := os.Remove(wrapper); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(bob, "domains", "b.example"), wrapper); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(alice, "domains", "a.example", "public_html")
		if err := MoveDirUnder(alice, src, carol, filepath.Join(wrapper, "public_html2")); err == nil {
			t.Fatal("moved through a link")
		}
		if !exists(src) || exists(filepath.Join(bob, "domains", "b.example", "public_html2")) {
			t.Fatal("the docroot left alice's home")
		}
	})
	t.Run("a docroot swapped for a link during the move is not left behind", func(t *testing.T) {
		_, alice, bob, carol := moveTree(t)
		src := filepath.Join(alice, "domains", "a.example", "public_html")
		dst := filepath.Join(carol, "domains", "a.example", "public_html")
		beforeMove = func() {
			if err := os.Rename(src, src+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(bob, "domains", "b.example", "public_html"), src); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { beforeMove = nil }()
		if err := MoveDirUnder(alice, src, carol, dst); err == nil {
			t.Fatal("a swapped-in link was moved without an error")
		}
		if exists(dst) || !exists(filepath.Join(bob, "domains", "b.example", "public_html", "index.html")) {
			t.Fatal("the link was left at the destination, or bob's docroot changed")
		}
	})
	t.Run("an existing destination is kept", func(t *testing.T) {
		_, alice, _, carol := moveTree(t)
		src := filepath.Join(alice, "domains", "a.example", "public_html")
		dst := filepath.Join(carol, "domains", "a.example", "public_html")
		if err := os.Mkdir(dst, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "keep"), nil, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := MoveDirUnder(alice, src, carol, dst); err == nil {
			t.Fatal("replaced an existing destination")
		}
		if !exists(src) || !exists(filepath.Join(dst, "keep")) {
			t.Fatal("source or destination changed")
		}
	})
}

func TestOpenExistingDirUnder(t *testing.T) {
	_, alice, bob, _ := moveTree(t)
	if f, err := OpenExistingDirUnder(alice, filepath.Join(alice, "domains", "missing")); err == nil {
		f.Close()
		t.Fatal("opened a folder that does not exist")
	}
	if exists(filepath.Join(alice, "domains", "missing")) {
		t.Fatal("created a missing folder")
	}
	link := filepath.Join(alice, "domains", "x.example")
	if err := os.Symlink(filepath.Join(bob, "domains"), link); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenExistingDirUnder(alice, filepath.Join(link, "b.example")); err == nil {
		f.Close()
		t.Fatal("opened through a link")
	}
	f, err := OpenExistingDirUnder(alice, filepath.Join(alice, "domains", "a.example"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestRemoveEmptyDirUnder(t *testing.T) {
	t.Run("removes an empty folder", func(t *testing.T) {
		_, alice, _, _ := moveTree(t)
		dir := filepath.Join(alice, "domains", "a.example")
		if err := os.Remove(filepath.Join(dir, "public_html")); err != nil {
			t.Fatal(err)
		}
		if err := RemoveEmptyDirUnder(alice, dir); err != nil || exists(dir) {
			t.Fatalf("empty folder not removed: %v", err)
		}
	})
	t.Run("keeps a folder with something in it", func(t *testing.T) {
		_, alice, _, _ := moveTree(t)
		dir := filepath.Join(alice, "domains", "a.example")
		if err := RemoveEmptyDirUnder(alice, dir); err == nil || !exists(dir) {
			t.Fatal("removed a folder that was not empty")
		}
	})
	t.Run("does not reach through a link", func(t *testing.T) {
		_, alice, bob, _ := moveTree(t)
		victim := filepath.Join(bob, "domains", "empty.example")
		if err := os.Mkdir(victim, 0o750); err != nil {
			t.Fatal(err)
		}
		domains := filepath.Join(alice, "domains")
		if err := os.RemoveAll(domains); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(bob, "domains"), domains); err != nil {
			t.Fatal(err)
		}
		if err := RemoveEmptyDirUnder(alice, filepath.Join(domains, "empty.example")); err == nil || !exists(victim) {
			t.Fatal("removed a folder through a link")
		}
	})
	t.Run("does not remove a link or its target", func(t *testing.T) {
		_, alice, bob, _ := moveTree(t)
		target := filepath.Join(bob, "domains", "empty.example")
		if err := os.Mkdir(target, 0o750); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(alice, "domains", "x.example")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := RemoveEmptyDirUnder(alice, link); err == nil || !exists(link) || !exists(target) {
			t.Fatal("removed a link or its target")
		}
	})
}
