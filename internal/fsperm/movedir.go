package fsperm

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// MoveDirUnder moves the directory src to dst. src's parent is reached from
// srcAnchor and dst's parent from dstAnchor, one component at a time without
// following a symlink; both parents must exist. src must be a directory, not a
// link, and dst must not exist. If src is swapped for something else between
// the check and the move, what landed at dst is removed (never followed) and
// the move fails. Both anchors must be directories the tenant can't swap for a
// link, as for ChownTree.
func MoveDirUnder(srcAnchor, src, dstAnchor, dst string) error {
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	sfd, err := openUnder(srcAnchor, filepath.Dir(src))
	if err != nil {
		return err
	}
	defer unix.Close(sfd)
	dfd, err := openUnder(dstAnchor, filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	sname, dname := filepath.Base(src), filepath.Base(dst)
	if err := isDirAt(sfd, sname); err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	if err := renameNoReplace(sfd, sname, dfd, dname); err != nil {
		return fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	if err := isDirAt(dfd, dname); err != nil {
		_ = unix.Unlinkat(dfd, dname, 0)
		return fmt.Errorf("%s changed during the move: %w", src, err)
	}
	return nil
}

// RemoveEmptyDirUnder removes dir if it is an empty directory. Its parent is
// reached from anchor without following a symlink, and a link at dir itself is
// not removed. A folder with something in it is left, and the call fails.
func RemoveEmptyDirUnder(anchor, dir string) error {
	dir = filepath.Clean(dir)
	pfd, err := openUnder(anchor, filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	return unix.Unlinkat(pfd, filepath.Base(dir), unix.AT_REMOVEDIR)
}

// isDirAt reports an error unless name in the directory dfd is a directory.
// It does not follow a link.
func isDirAt(dfd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("not a directory")
	}
	return nil
}

// renameNoReplace renames without replacing an existing target. A filesystem
// without RENAME_NOREPLACE gets a plain renameat after checking the target is
// absent.
func renameNoReplace(sfd int, sname string, dfd int, dname string) error {
	err := unix.Renameat2(sfd, sname, dfd, dname, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	var st unix.Stat_t
	if serr := unix.Fstatat(dfd, dname, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(serr, unix.ENOENT) {
		return unix.EEXIST
	}
	return unix.Renameat(sfd, sname, dfd, dname)
}
