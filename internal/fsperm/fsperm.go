// Package fsperm applies group-ownership + setgid to web docroots in a
// way that is safe to run AS ROOT over a tenant-writable directory tree.
//
// jabali serves tenant files as <user>:www-data 0640 and nginx (www-data)
// reads them via the www-data group; per-user PHP-FPM writes new files as
// <user>:<user>, so docroots must be group www-data + setgid for fresh
// uploads to be readable (see feedback: docroot-setgid-www-data).
//
// SECURITY (TOCTOU): a naive `filepath.Walk` + path-based `os.Chmod`
// is exploitable here — the tenant owns the tree, so between Walk's
// Lstat and a later os.Chmod(path) they can swap a directory (or any
// intermediate path component) for a symlink and redirect the
// privileged chmod onto an arbitrary target (e.g. setgid on a dir
// outside their home). O_NOFOLLOW on the final component is not enough,
// because the *intermediate* components of a full path string are still
// resolved (and followed) by the kernel.
//
// This package therefore never passes a multi-component tenant path to a
// privileged syscall. It opens a trusted anchor (the user's home) with
// O_NOFOLLOW|O_DIRECTORY and descends one component at a time with
// openat(parentfd, name, O_NOFOLLOW|O_DIRECTORY); every chown/chmod
// targets a verified file descriptor (Fchown/Fchmod) or a single
// component relative to a verified dir fd (Fchownat AT_SYMLINK_NOFOLLOW).
// A component that was raced to a symlink/non-dir fails the openat and is
// skipped — the operation can only ever land inside the real tree.
package fsperm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const openFlags = unix.O_NOFOLLOW | unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC

// applyDirFd sets group=gid (owner preserved) and the setgid bit on the
// directory referenced by dfd. Operates on the fd only — no path.
func applyDirFd(dfd, gid int) error {
	if err := unix.Fchown(dfd, -1, gid); err != nil {
		return fmt.Errorf("fchown: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(dfd, &st); err != nil {
		return fmt.Errorf("fstat: %w", err)
	}
	if err := unix.Fchmod(dfd, st.Mode&0o7777|unix.S_ISGID); err != nil {
		return fmt.Errorf("fchmod: %w", err)
	}
	return nil
}

// readEntryNames lists the immediate entry names of the dir at dfd using
// a fresh openat(".") fd, so the caller's dfd offset is untouched and no
// symlink is followed.
func readEntryNames(dfd int) ([]string, error) {
	rfd, err := unix.Openat(dfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(rfd), ".")
	defer f.Close()
	return f.Readdirnames(-1)
}

// walkApply recurses the tree rooted at the directory fd dfd, applying
// group+setgid to every real directory and group-only (no setgid) to
// every other non-symlink entry. Symlinks are never touched. Entries
// raced to a symlink/non-dir between the fstatat and the openat are
// skipped, never followed.
func walkApply(dfd, gid int) error {
	if err := applyDirFd(dfd, gid); err != nil {
		return err
	}
	names, err := readEntryNames(dfd)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "." || name == ".." {
			continue
		}
		var cst unix.Stat_t
		if err := unix.Fstatat(dfd, name, &cst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // vanished — skip
		}
		switch cst.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			continue // never follow / touch a tenant symlink
		case unix.S_IFDIR:
			cfd, err := unix.Openat(dfd, name, openFlags, 0)
			if err != nil {
				continue // raced to symlink/non-dir — skip safely
			}
			err = walkApply(cfd, gid)
			unix.Close(cfd)
			if err != nil {
				return err
			}
		default:
			// Regular file etc: flip group only, no symlink follow.
			if err := unix.Fchownat(dfd, name, -1, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return fmt.Errorf("fchownat %s: %w", name, err)
			}
		}
	}
	return nil
}

// GroupSetgidTree applies group=gid + setgid to the directory tree rooted
// at root, symlink-safely. root itself must be a real directory (opened
// O_NOFOLLOW); if it was swapped for a symlink the call fails rather than
// following it.
func GroupSetgidTree(root string, gid int) error {
	fd, err := unix.Open(root, openFlags, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", root, err)
	}
	defer unix.Close(fd)
	return walkApply(fd, gid)
}

// RepairDocrootGroup makes a web docroot readable by nginx (www-data)
// without following any tenant symlink. It anchors at userHome (a
// trusted, root-or-user-owned dir that it never modifies), descends each
// path component from userHome to docroot via openat O_NOFOLLOW applying
// group+setgid to the traversal-chain dirs (so www-data keeps +x to
// reach the root), then recurses the docroot subtree.
//
// docroot must be strictly under userHome. gid is the www-data gid.
func RepairDocrootGroup(userHome, docroot string, gid int) error {
	rel, err := filepath.Rel(filepath.Clean(userHome), filepath.Clean(docroot))
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("docroot %q is not under %q", docroot, userHome)
	}

	anchor, err := unix.Open(filepath.Clean(userHome), openFlags, 0)
	if err != nil {
		return fmt.Errorf("open anchor %s: %w", userHome, err)
	}
	defer unix.Close(anchor)

	comps := strings.Split(rel, string(os.PathSeparator))
	fds := make([]int, 0, len(comps))
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}()

	cur := anchor
	for _, comp := range comps {
		child, oerr := unix.Openat(cur, comp, openFlags, 0)
		if oerr != nil {
			return fmt.Errorf("descend %s: %w", comp, oerr)
		}
		fds = append(fds, child)
		cur = child
	}

	// Apply to the intermediate chain dirs (everything but the docroot
	// leaf); the leaf + its subtree are handled by walkApply.
	for i := 0; i < len(fds)-1; i++ {
		if err := applyDirFd(fds[i], gid); err != nil {
			return err
		}
	}
	return walkApply(cur, gid)
}

// ChownTree sets owner uid and group gid on root and everything under it,
// AS ROOT over a tenant-writable tree. A symlink is chowned itself, never
// followed.
//
// root is reached from anchor one component at a time (openat O_NOFOLLOW),
// so a symlink planted between anchor and root fails the call instead of
// redirecting it. anchor must be a directory the tenant can't swap for a
// link (their home: /home belongs to root). root may equal anchor.
//
// SECURITY (TOCTOU): the walk is by file descriptor, the same way as
// walkApply. A directory the tenant swaps for a symlink after it was seen is
// chowned as the link (AT_SYMLINK_NOFOLLOW), and the walk never enters it.
func ChownTree(anchor, root string, uid, gid int) error {
	fd, err := openUnder(anchor, root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return walkChown(fd, uid, gid)
}

// openUnder opens the directory root, descending from anchor one component
// at a time without following a symlink.
func openUnder(anchor, root string) (int, error) {
	anchor, root = filepath.Clean(anchor), filepath.Clean(root)
	rel, err := filepath.Rel(anchor, root)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return -1, fmt.Errorf("%q is not under %q", root, anchor)
	}
	cur, err := unix.Open(anchor, openFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", anchor, err)
	}
	if rel == "." {
		return cur, nil
	}
	for _, comp := range strings.Split(rel, string(os.PathSeparator)) {
		child, oerr := unix.Openat(cur, comp, openFlags, 0)
		unix.Close(cur)
		if oerr != nil {
			return -1, fmt.Errorf("open %s under %s: %w", comp, anchor, oerr)
		}
		cur = child
	}
	return cur, nil
}

// beforeDescend, when set, runs between seeing a directory entry and opening
// it. Tests use it to swap the entry, as a racing tenant would.
var beforeDescend func(dfd int, name string)

// walkChown chowns the directory at dfd and, recursively, every entry in it.
func walkChown(dfd, uid, gid int) error {
	if err := unix.Fchown(dfd, uid, gid); err != nil {
		return fmt.Errorf("fchown: %w", err)
	}
	names, err := readEntryNames(dfd)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "." || name == ".." {
			continue
		}
		var cst unix.Stat_t
		if err := unix.Fstatat(dfd, name, &cst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // vanished — skip
		}
		if cst.Mode&unix.S_IFMT == unix.S_IFDIR {
			if beforeDescend != nil {
				beforeDescend(dfd, name)
			}
			cfd, oerr := unix.Openat(dfd, name, openFlags, 0)
			if oerr == nil {
				err = walkChown(cfd, uid, gid)
				unix.Close(cfd)
				if err != nil {
					return err
				}
				continue
			}
			// No longer a directory (raced to a symlink or a file): chown
			// the entry itself below, without following it.
		}
		if err := unix.Fchownat(dfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return fmt.Errorf("fchownat %s: %w", name, err)
		}
	}
	return nil
}
