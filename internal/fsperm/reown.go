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
	// Linked counts the matched files left alone because the walk could not
	// confirm that all of their hard links are inside it.
	Linked int
}

// maxLinkSightings caps the links of multiply-linked files a walk remembers
// (each holds a descriptor) until it can tell whether all were inside it.
const maxLinkSightings = 4096

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
// A matched file with more than one hard link changes only when all of its
// links are inside the walk, so a link from elsewhere (another tenant's home)
// never moves the file to this owner. The file is held, with every place the
// walk saw it, until the walk ends. It then changes only if each of those
// names still leads to it, none of their directories changed since it was
// listed, and the distinct names number its link count. A link moved during
// the walk changes its old directory, so it can't be counted twice. Files
// that don't pass are counted in Linked.
//
// stop is checked before each entry; once it returns true the walk ends with
// Stopped set. A nil stop never stops. The error is set only when root itself
// cannot be opened; problems with entries are counted in the stats.
func ReownTree(anchor, root string, match func(owner uint32) bool, uid int, newGID func(group uint32) int, stop func() bool) (ReownStats, error) {
	return ReownTrees([]ReownRoot{{Anchor: anchor, Path: root, Match: match}}, uid, newGID, stop)
}

// ReownRoot is one tree of a ReownTrees walk.
type ReownRoot struct {
	// Anchor and Path are ReownTree's anchor and root.
	Anchor, Path string
	// Match accepts the owners whose entries change in this tree.
	Match func(owner uint32) bool
	// Enter, when set, says from a directory's fstat whether to walk into
	// it; the root is one of them. Nil walks into every directory.
	Enter func(dir *unix.Stat_t) bool
	// ChangeIn, when set, says from a directory's fstat whether its entries
	// may change. Nil lets every entry change, the root included; with
	// ChangeIn set the root itself never changes.
	ChangeIn func(dir *unix.Stat_t) bool
}

// ReownTrees is ReownTree over several trees in one walk. The hard-link
// check spans all of them: a file with links in two of the trees changes
// when every link is in one of them and each tree it is in matches it. A
// tree other than the first is skipped when it doesn't exist or is on
// another filesystem. The error is set only when the first root cannot be
// opened; problems with the others are counted in the stats.
func ReownTrees(roots []ReownRoot, uid int, newGID func(group uint32) int, stop func() bool) (ReownStats, error) {
	st := ReownStats{Reowned: map[uint32]int{}}
	if len(roots) == 0 {
		return st, nil
	}
	var w *reowner
	defer func() {
		if w != nil {
			w.close()
		}
	}()
	for i, r := range roots {
		fd, err := openUnder(r.Anchor, r.Path)
		if err != nil {
			if i == 0 {
				return st, err
			}
			if !errors.Is(err, unix.ENOENT) {
				st.fail("open "+r.Path, err)
			}
			continue
		}
		var rst unix.Stat_t
		if err := unix.Fstat(fd, &rst); err != nil {
			unix.Close(fd)
			if i == 0 {
				return st, fmt.Errorf("fstat %s: %w", r.Path, err)
			}
			st.fail("stat "+r.Path, err)
			continue
		}
		if w == nil {
			w = newReowner(rst.Dev, r.Match, uid, newGID, stop, &st)
		} else if rst.Dev != w.dev || w.stopped() {
			unix.Close(fd)
			continue
		}
		w.match, w.enter, w.changeIn = r.Match, r.Enter, r.ChangeIn
		if w.enter == nil || w.enter(&rst) {
			if w.changeIn == nil {
				w.entry(fd, &rst, r.Path, -1, nil)
			} else {
				st.Scanned++
			}
			w.walk(fd)
		}
		unix.Close(fd)
	}
	if w != nil {
		w.settleLinked()
	}
	return st, nil
}

type reowner struct {
	dev       uint64
	match     func(uint32) bool
	enter     func(*unix.Stat_t) bool
	changeIn  func(*unix.Stat_t) bool
	uid       int
	newGID    func(uint32) int
	stop      func() bool
	st        *ReownStats
	linked    map[uint64]*heldLink // by inode
	dirs      map[uint64]*heldDir  // by inode
	sightings int
}

func newReowner(dev uint64, match func(uint32) bool, uid int, newGID func(uint32) int, stop func() bool, st *ReownStats) *reowner {
	return &reowner{dev: dev, match: match, uid: uid, newGID: newGID, stop: stop, st: st,
		linked: map[uint64]*heldLink{}, dirs: map[uint64]*heldDir{}}
}

// heldLink is a matched multiply-linked file: a descriptor to it, every
// place the walk saw it, and the matches of the trees it was seen in.
type heldLink struct {
	fd      int
	name    string
	links   []linkSighting
	matches []func(uint32) bool
}

type linkSighting struct {
	dir  *heldDir
	name string
}

// heldDir is a directory a held file was seen in: a descriptor to it and its
// ctime from just before it was listed.
type heldDir struct {
	fd    int
	ino   uint64
	ctime unix.Timespec
}

func (w *reowner) close() {
	for _, h := range w.linked {
		unix.Close(h.fd)
	}
	for _, d := range w.dirs {
		unix.Close(d.fd)
	}
	w.linked, w.dirs = nil, nil
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
// fd's own fstat; parent and pst are the directory it was listed in and that
// directory's fstat from before the listing. A multiply-linked non-directory
// is held instead (see ReownTree); held reports that w took fd and the caller
// must not close it.
func (w *reowner) entry(fd int, st *unix.Stat_t, name string, parent int, pst *unix.Stat_t) (held bool) {
	w.st.Scanned++
	if !w.match(st.Uid) {
		return false
	}
	if st.Nlink > 1 && st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return w.hold(fd, st, name, parent, pst)
	}
	w.change(fd, st, name)
	return false
}

// hold records one sighting of a multiply-linked file.
func (w *reowner) hold(fd int, st *unix.Stat_t, name string, parent int, pst *unix.Stat_t) (held bool) {
	h := w.linked[st.Ino]
	if w.sightings >= maxLinkSightings || pst == nil {
		if h != nil {
			h.links = nil // can't account for every link any more
		} else {
			w.st.Linked++
		}
		return false
	}
	d := w.dirs[pst.Ino]
	if d == nil {
		dup, err := unix.FcntlInt(uintptr(parent), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			w.st.fail("hold directory of "+name, err)
			return false
		}
		d = &heldDir{fd: dup, ino: pst.Ino, ctime: pst.Ctim}
		w.dirs[pst.Ino] = d
	}
	w.sightings++
	if h == nil {
		h = &heldLink{fd: fd, name: name}
		w.linked[st.Ino] = h
		held = true
	}
	h.links = append(h.links, linkSighting{dir: d, name: name})
	h.matches = append(h.matches, w.match)
	return held
}

// settleLinked changes each held file whose links are confirmed all inside
// the walk, through its held descriptor, and counts the rest in Linked.
func (w *reowner) settleLinked() {
	for ino, h := range w.linked {
		var st unix.Stat_t
		if err := unix.Fstat(h.fd, &st); err != nil {
			w.st.fail("stat "+h.name, err)
			continue
		}
		if !h.matchesAll(st.Uid) {
			continue
		}
		if w.allLinksSeen(ino, h, &st) {
			w.change(h.fd, &st, h.name)
		} else {
			w.st.Linked++
		}
	}
}

// matchesAll reports whether the owner is still one that every tree the
// file was seen in would change.
func (h *heldLink) matchesAll(owner uint32) bool {
	for _, m := range h.matches {
		if !m(owner) {
			return false
		}
	}
	return len(h.matches) > 0
}

// allLinksSeen reports whether every sighting of the held file still names
// it in a directory unchanged since it was listed, and the distinct names
// number its link count.
func (w *reowner) allLinksSeen(ino uint64, h *heldLink, st *unix.Stat_t) bool {
	if len(h.links) == 0 {
		return false
	}
	type place struct {
		dir  uint64
		name string
	}
	seen := map[place]bool{}
	for _, l := range h.links {
		var ds, ls unix.Stat_t
		if unix.Fstat(l.dir.fd, &ds) != nil || ds.Ctim != l.dir.ctime {
			return false
		}
		if unix.Fstatat(l.dir.fd, l.name, &ls, unix.AT_SYMLINK_NOFOLLOW) != nil || ls.Dev != w.dev || ls.Ino != ino {
			return false
		}
		seen[place{l.dir.ino, l.name}] = true
	}
	return uint64(len(seen)) == uint64(st.Nlink)
}

// change gives the inode behind fd to w.uid (and the group newGID picks).
func (w *reowner) change(fd int, st *unix.Stat_t, name string) {
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
	// The directory's ctime from before the listing: a held file's sighting
	// here counts only if the directory is unchanged at the end.
	var dst unix.Stat_t
	if err := unix.Fstat(dfd, &dst); err != nil {
		w.st.fail("stat directory", err)
		return
	}
	names, err := readEntryNames(dfd)
	if err != nil {
		w.st.fail("list directory", err)
		return
	}
	canChange := w.changeIn == nil || w.changeIn(&dst)
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
		if !canChange {
			w.st.Scanned++
		} else if w.entry(efd, &est, name, dfd, &dst) {
			continue // held until the walk ends
		}
		if est.Mode&unix.S_IFMT != unix.S_IFDIR {
			unix.Close(efd)
			continue
		}
		if w.enter != nil {
			// Decided on the directory as it is now: entry may just have
			// changed its owner.
			var cur unix.Stat_t
			if err := unix.Fstat(efd, &cur); err != nil || !w.enter(&cur) {
				unix.Close(efd)
				continue
			}
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
