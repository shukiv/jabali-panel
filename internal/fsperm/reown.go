package fsperm

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// ReownStats reports one ReownTree walk.
type ReownStats struct {
	// Reowned counts the entries handed over, by their previous owner.
	Reowned map[uint32]int
	// Scanned counts the entries looked at, root included.
	Scanned int
	// Stopped is set when stop ended the walk before it was done.
	Stopped bool
	// Failed counts the entries that could not be read or changed;
	// FirstError describes the first of them.
	Failed     int
	FirstError string
}

func (s *ReownStats) fail(what string, err error) {
	s.Failed++
	if s.FirstError == "" {
		s.FirstError = fmt.Sprintf("%s: %v", what, err)
	}
}

// Test hooks. afterOwnerRead runs between reading an entry's owner and
// changing it (tests rename another file into the entry's place there, as a
// racing tenant would); fchownFd is the change itself.
var (
	afterOwnerRead func(dfd int, name string)
	fchownFd       = func(fd, uid, gid int) error {
		return unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
	}
)

// ReownTree gives uid every entry under root, root included, whose owner
// match accepts. root is reached from anchor one component at a time without
// following a link (see openUnder), and the walk never leaves root's
// filesystem. An entry's group becomes newGID(group) when that is >= 0 and is
// kept otherwise, so a docroot's www-data group stays; a nil newGID keeps
// every group.
//
// Unlike ChownTree, whether an entry changes depends on the owner read first,
// so the read and the change must reach the same inode. Each entry is opened
// O_PATH|O_NOFOLLOW, and the fstat and the fchownat (AT_EMPTY_PATH) both go
// through that descriptor: a file renamed into the entry's place in between
// is never touched, and a symlink is changed as the link, never followed.
// Directories are listed through a descriptor reopened from the same one.
//
// stop is checked before each entry; once it returns true the walk ends with
// Stopped set. A nil stop never stops. The error is set only when root itself
// cannot be opened; problems with entries are counted in the stats.
func ReownTree(anchor, root string, match func(owner uint32) bool, uid int, newGID func(group uint32) int, stop func() bool) (ReownStats, error) {
	st := ReownStats{Reowned: map[uint32]int{}}
	fd, err := openUnder(anchor, root)
	if err != nil {
		return st, err
	}
	defer unix.Close(fd)
	var rst unix.Stat_t
	if err := unix.Fstat(fd, &rst); err != nil {
		return st, fmt.Errorf("fstat %s: %w", root, err)
	}
	w := &reowner{dev: rst.Dev, match: match, uid: uid, newGID: newGID, stop: stop, st: &st}
	w.entry(fd, &rst, root)
	w.walk(fd)
	return st, nil
}

type reowner struct {
	dev    uint64
	match  func(uint32) bool
	uid    int
	newGID func(uint32) int
	stop   func() bool
	st     *ReownStats
}

func (w *reowner) stopped() bool {
	if w.st.Stopped {
		return true
	}
	if w.stop != nil && w.stop() {
		w.st.Stopped = true
	}
	return w.st.Stopped
}

// entry changes the inode behind fd when match accepts its owner. st must be
// fd's own fstat.
func (w *reowner) entry(fd int, st *unix.Stat_t, name string) {
	w.st.Scanned++
	if !w.match(st.Uid) {
		return
	}
	gid := -1
	if w.newGID != nil {
		gid = w.newGID(st.Gid)
	}
	if err := fchownFd(fd, w.uid, gid); err != nil {
		w.st.fail("chown "+name, err)
		return
	}
	w.st.Reowned[st.Uid]++
}

// walk handles every entry of the directory at dfd and, recursively, the
// directories among them.
func (w *reowner) walk(dfd int) {
	names, err := readEntryNames(dfd)
	if err != nil {
		w.st.fail("list directory", err)
		return
	}
	for _, name := range names {
		if w.stopped() {
			return
		}
		if name == "." || name == ".." {
			continue
		}
		efd, err := unix.Openat(dfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if !errors.Is(err, unix.ENOENT) { // gone meanwhile: nothing to change
				w.st.fail("open "+name, err)
			}
			continue
		}
		var est unix.Stat_t
		if err := unix.Fstat(efd, &est); err != nil {
			unix.Close(efd)
			w.st.fail("stat "+name, err)
			continue
		}
		if est.Dev != w.dev { // another filesystem is mounted here
			unix.Close(efd)
			continue
		}
		if afterOwnerRead != nil {
			afterOwnerRead(dfd, name)
		}
		w.entry(efd, &est, name)
		if est.Mode&unix.S_IFMT != unix.S_IFDIR {
			unix.Close(efd)
			continue
		}
		// An O_PATH descriptor can't be listed; "." reopens the same inode.
		sub, err := unix.Openat(efd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(efd)
		if err != nil {
			w.st.fail("open directory "+name, err)
			continue
		}
		w.walk(sub)
		unix.Close(sub)
	}
}
