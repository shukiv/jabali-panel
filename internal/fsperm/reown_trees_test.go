package fsperm

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func never(uint32) bool { return false }

func changedOnce(t *testing.T, inodes []uint64, path string) bool {
	t.Helper()
	n := 0
	for _, ino := range inodes {
		if ino == inode(t, path) {
			n++
		}
	}
	if n > 1 {
		t.Errorf("%s changed %d times", path, n)
	}
	return n == 1
}

// A file linked from both trees is seen whole, so it changes once.
func TestReownTrees_HardLinkAcrossTreesChanges(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	a := filepath.Join(home, "scan.pdf")
	write(t, a)
	if err := os.Link(a, filepath.Join(tmp, "moved.pdf")); err != nil {
		t.Fatal(err)
	}
	inodes := recordChowns(t)

	st, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: ownedByMe},
		{Anchor: tmp, Path: tmp, Match: ownedByMe},
	}, os.Getuid(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changedOnce(t, *inodes, a) || st.Linked != 0 {
		t.Errorf("Linked = %d, changed = %v; want the file changed once", st.Linked, changedOnce(t, *inodes, a))
	}
}

// A link in a tree whose Match refuses the owner keeps the file as it is.
func TestReownTrees_ALinkInATreeThatDoesNotMatchStays(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	a := filepath.Join(home, "scan.pdf")
	write(t, a)
	if err := os.Link(a, filepath.Join(tmp, "moved.pdf")); err != nil {
		t.Fatal(err)
	}
	inodes := recordChowns(t)

	st, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: ownedByMe},
		{Anchor: tmp, Path: tmp, Match: never},
	}, os.Getuid(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedOnce(t, *inodes, a) || st.Linked != 1 {
		t.Errorf("Linked = %d; want the file left as it is and counted", st.Linked)
	}
}

// Enter keeps the walk out of a directory; ChangeIn keeps a directory's
// entries, and the root, as they are.
func TestReownTrees_EnterAndChangeIn(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	open, closed, ro := filepath.Join(tmp, "open"), filepath.Join(tmp, "closed"), filepath.Join(tmp, "ro")
	f, g, h := filepath.Join(open, "f"), filepath.Join(closed, "g"), filepath.Join(ro, "h")
	for _, p := range []string{f, g, h} {
		write(t, p)
	}
	closedIno, roIno := inode(t, closed), inode(t, ro)
	inodes := recordChowns(t)

	st, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: never},
		{
			Anchor:   tmp,
			Path:     tmp,
			Match:    ownedByMe,
			Enter:    func(d *unix.Stat_t) bool { return d.Ino != closedIno },
			ChangeIn: func(d *unix.Stat_t) bool { return d.Ino != roIno },
		},
	}, os.Getuid(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{open, f, closed, ro} {
		if !changedOnce(t, *inodes, p) {
			t.Errorf("%s did not change", p)
		}
	}
	for _, p := range []string{tmp, g, h} {
		if changedOnce(t, *inodes, p) {
			t.Errorf("%s changed", p)
		}
	}
	if st.Failed != 0 {
		t.Errorf("Failed = %d (%s)", st.Failed, st.FirstError)
	}
}

func TestReownTrees_AMissingTreeIsSkipped(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "scan.pdf"))
	recordChowns(t)
	st, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: ownedByMe},
		{Anchor: tmp, Path: filepath.Join(tmp, "gone"), Match: ownedByMe},
	}, os.Getuid(), nil, nil)
	if err != nil || st.Failed != 0 || st.Reowned[uint32(os.Getuid())] != 2 {
		t.Errorf("err %v, stats %+v; want the home changed and the missing tree skipped", err, st)
	}
}

func TestReownTrees_TheFirstRootMustOpen(t *testing.T) {
	tmp := t.TempDir()
	if _, err := ReownTrees([]ReownRoot{{Anchor: tmp, Path: filepath.Join(tmp, "gone"), Match: ownedByMe}}, os.Getuid(), nil, nil); err == nil {
		t.Error("a missing first root gave no error")
	}
}

// A tree on another filesystem is skipped: its links can't be the first
// tree's.
func TestReownTrees_ATreeOnAnotherFilesystemIsSkipped(t *testing.T) {
	home := t.TempDir()
	other, err := os.MkdirTemp("/dev/shm", "reowntrees")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	t.Cleanup(func() { os.RemoveAll(other) })
	var hs, os2 unix.Stat_t
	if unix.Stat(home, &hs) != nil || unix.Stat(other, &os2) != nil || hs.Dev == os2.Dev {
		t.Skip("/dev/shm is on the test directory's filesystem")
	}
	x := filepath.Join(other, "x")
	write(t, x)
	inodes := recordChowns(t)
	if _, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: ownedByMe},
		{Anchor: other, Path: other, Match: ownedByMe},
	}, os.Getuid(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if changedOnce(t, *inodes, x) || changedOnce(t, *inodes, other) {
		t.Error("an entry on another filesystem changed")
	}
}

// A root Enter refuses is left whole, the root included.
func TestReownTrees_ARootItMayNotEnterIsLeft(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	f := filepath.Join(tmp, "f")
	write(t, f)
	inodes := recordChowns(t)
	if _, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: never},
		{Anchor: tmp, Path: tmp, Match: ownedByMe, Enter: func(*unix.Stat_t) bool { return false }},
	}, os.Getuid(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if changedOnce(t, *inodes, tmp) || changedOnce(t, *inodes, f) {
		t.Error("changed inside a root the walk may not enter")
	}
}

// Before a held file changes its owner is checked again against every tree
// it was seen in, not only the first.
func TestReownTrees_AHeldFileIsCheckedAgainstEveryTree(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	a := filepath.Join(home, "scan.pdf")
	write(t, a)
	if err := os.Link(a, filepath.Join(tmp, "moved.pdf")); err != nil {
		t.Fatal(err)
	}
	inodes := recordChowns(t)
	asked := 0
	// Takes the file when it is seen, not when asked again.
	firstOnly := func(owner uint32) bool { asked++; return asked == 1 && ownedByMe(owner) }

	st, err := ReownTrees([]ReownRoot{
		{Anchor: home, Path: home, Match: ownedByMe},
		{Anchor: tmp, Path: tmp, Match: firstOnly, ChangeIn: func(*unix.Stat_t) bool { return true }},
	}, os.Getuid(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if asked < 2 {
		t.Fatalf("the second tree's Match was asked %d times, want again before the change", asked)
	}
	// No longer one to change: left as it is, and not counted as held back
	// by a link.
	if changedOnce(t, *inodes, a) || st.Linked != 0 {
		t.Errorf("Linked = %d; want the file left as it is", st.Linked)
	}
}
