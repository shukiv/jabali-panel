package fsperm

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// reownTree builds a home with a nested file, a directory, and a link to a
// file outside the home. It returns the home, every path in it (home
// included), and the outside file.
func reownTree(t *testing.T) (home string, inside []string, outFile string) {
	t.Helper()
	home = t.TempDir()
	file := filepath.Join(home, "domains", "a.test", "public_html", "scan.pdf")
	write(t, file)
	_, outFile = outsideTree(t)
	link := filepath.Join(home, "domains", "a.test", "evil")
	if err := os.Symlink(outFile, link); err != nil {
		t.Fatal(err)
	}
	inside = []string{
		home,
		filepath.Join(home, "domains"),
		filepath.Join(home, "domains", "a.test"),
		filepath.Join(home, "domains", "a.test", "public_html"),
		file,
		link,
	}
	return home, inside, outFile
}

func ownedByMe(owner uint32) bool { return int(owner) == os.Getuid() }

func TestReownTree_ChangesMatchedEntriesWithoutFollowingLinks(t *testing.T) {
	gid := otherGroup(t)
	home, inside, outFile := reownTree(t)

	st, err := ReownTree(home, home, ownedByMe, os.Getuid(), func(uint32) int { return gid }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range inside {
		if got := gidOf(t, p); got != gid {
			t.Errorf("%s: gid %d, want %d", p, got, gid)
		}
	}
	if got := gidOf(t, outFile); got != os.Getgid() {
		t.Errorf("the file outside the home was changed through a link (gid %d)", got)
	}
	if st.Reowned[uint32(os.Getuid())] != len(inside) || st.Scanned != len(inside) || st.Failed != 0 || st.Stopped {
		t.Errorf("stats = %+v, want %d reowned and scanned", st, len(inside))
	}
}

// The real change, to the test user's own uid and gid (allowed without root):
// fchownat with AT_EMPTY_PATH works on the O_PATH descriptors, the link's
// included.
func TestReownTree_RealChangeThroughTheDescriptors(t *testing.T) {
	home, inside, _ := reownTree(t)
	st, err := ReownTree(home, home, ownedByMe, os.Getuid(), func(uint32) int { return os.Getgid() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Failed != 0 || st.Reowned[uint32(os.Getuid())] != len(inside) {
		t.Errorf("stats = %+v, want all %d changed", st, len(inside))
	}
}

func TestReownTree_LeavesOtherOwnersAlone(t *testing.T) {
	gid := otherGroup(t)
	home, inside, _ := reownTree(t)

	st, err := ReownTree(home, home, func(uint32) bool { return false }, os.Getuid(), func(uint32) int { return gid }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range inside {
		if got := gidOf(t, p); got != os.Getgid() {
			t.Errorf("%s: owner not matched but gid changed to %d", p, got)
		}
	}
	// Directories whose owner doesn't match are still walked.
	if len(st.Reowned) != 0 || st.Scanned != len(inside) {
		t.Errorf("stats = %+v, want nothing reowned and %d scanned", st, len(inside))
	}
}

func TestReownTree_KeepsTheGroupWhenAsked(t *testing.T) {
	gid := otherGroup(t)
	home, inside, _ := reownTree(t)
	file := inside[4]
	if err := os.Chown(file, -1, gid); err != nil {
		t.Fatal(err)
	}

	// The www-data case: keep any group but the deleted account's own.
	keepGID := func(g uint32) int {
		if int(g) == gid {
			return -1
		}
		return os.Getgid()
	}
	if _, err := ReownTree(home, home, ownedByMe, os.Getuid(), keepGID, nil); err != nil {
		t.Fatal(err)
	}
	if got := gidOf(t, file); got != gid {
		t.Errorf("kept group was changed to %d", got)
	}
}

// recordChowns replaces the change with a recorder of the inodes it reached.
func recordChowns(t *testing.T) *[]uint64 {
	t.Helper()
	var inodes []uint64
	orig := fchownFd
	fchownFd = func(fd, _, _ int) error {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		inodes = append(inodes, st.Ino)
		return nil
	}
	t.Cleanup(func() { fchownFd = orig })
	return &inodes
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// A tenant renames another file into the entry's place after its owner was
// read. The change must still reach the entry that was read, never the file
// moved in.
func TestReownTree_RenameAfterTheOwnerReadChangesNothingElse(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "target")
	write(t, target)
	decoy := filepath.Join(t.TempDir(), "decoy")
	write(t, decoy)
	targetIno, decoyIno := inode(t, target), inode(t, decoy)

	inodes := recordChowns(t)
	afterOwnerRead = func(dfd int, name string) {
		if name == "target" {
			if err := os.Rename(decoy, target); err != nil {
				t.Errorf("swap: %v", err)
			}
		}
	}
	t.Cleanup(func() { afterOwnerRead = nil })

	if _, err := ReownTree(home, home, ownedByMe, os.Getuid(), nil, nil); err != nil {
		t.Fatal(err)
	}
	got := map[uint64]bool{}
	for _, ino := range *inodes {
		got[ino] = true
	}
	if !got[targetIno] {
		t.Error("the entry whose owner was read was not changed")
	}
	if got[decoyIno] {
		t.Error("the file renamed into place was changed")
	}
}

func TestReownTree_StopEndsTheWalk(t *testing.T) {
	home, inside, _ := reownTree(t)
	recordChowns(t)
	checks := 0
	st, err := ReownTree(home, home, ownedByMe, os.Getuid(), nil, func() bool {
		checks++
		return checks > 2
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stopped || st.Scanned >= len(inside) {
		t.Errorf("stats = %+v, want stopped before all %d entries", st, len(inside))
	}
}

func TestReownTree_CountsFailures(t *testing.T) {
	home, inside, _ := reownTree(t)
	orig := fchownFd
	fchownFd = func(int, int, int) error { return unix.EDQUOT }
	t.Cleanup(func() { fchownFd = orig })

	st, err := ReownTree(home, home, ownedByMe, os.Getuid(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Failed != len(inside) || len(st.Reowned) != 0 || st.FirstError == "" {
		t.Errorf("stats = %+v, want %d failures and nothing reowned", st, len(inside))
	}
}

func TestReownTree_RefusesARootReachedThroughALink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, "data")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReownTree(home, filepath.Join(home, "data"), ownedByMe, os.Getuid(), nil, nil); !errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
		t.Errorf("err = %v, want the link refused", err)
	}
	if _, err := ReownTree(home, filepath.Dir(home), ownedByMe, os.Getuid(), nil, nil); err == nil {
		t.Error("a root above the anchor was accepted")
	}
}
