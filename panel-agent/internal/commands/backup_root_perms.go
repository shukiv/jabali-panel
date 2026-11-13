package commands

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// backup_root_perms.go — /var/lib/jabali-backups belongs to root.
//
// The agent stages every restore under restore-staging and applies what it
// staged as root; it also keeps its locks, the local restic repo and the
// full-server packing folders there. Only root may own those, so nothing else
// can rename or replace them while the agent works. install.sh revisions before
// this one gave /var/lib/jabali/backups (what /var/lib/jabali-backups links to)
// to the panel's user on every `jabali update`.
//
// The folder is root-owned, group jabali, 0750: the panel reads downloads/ and
// logs/ through it. restore-staging is root-only, 0700. Below the folder, a link
// that isn't root's is removed (the agent never makes one), a folder that isn't
// root's goes back to root, and so does a lock file. Every step works from a
// directory handle, so a link put in place of a folder is never followed.

var (
	backupsRootDir = "/var/lib/jabali-backups"
	// Test seams: the owner every entry must have, the chown call, and the
	// panel group's id.
	backupsOwnerUID = 0
	backupsFchown   = unix.Fchown
	backupsGroupGID = func() (int, error) {
		g, err := user.LookupGroup(serviceGroupName())
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(g.Gid)
	}
)

const restoreStagingName = "restore-staging"

// restoreStagingRoot is the root-only parent of every restore stage.
func restoreStagingRoot() string { return filepath.Join(backupsRootDir, restoreStagingName) }

// SecureBackupsRoot runs once at agent start. A failure is logged; every
// handler that stages a restore runs the same check again and refuses to stage
// when it fails.
func SecureBackupsRoot(log *slog.Logger) {
	if err := ensureRestoreStaging(); err != nil {
		log.Warn("backups folder: could not give it back to root", "dir", backupsRootDir, "err", err)
	}
}

// ensureRestoreStaging gives the backups folder and its restore-staging back to
// root. Call it before staging a restore.
func ensureRestoreStaging() error {
	gid, err := backupsGroupGID()
	if err != nil {
		return fmt.Errorf("look up group %s: %w", serviceGroupName(), err)
	}
	if err := os.MkdirAll(backupsRootDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", backupsRootDir, err)
	}
	root, err := openDirRootLinksOnly(backupsRootDir)
	if err != nil {
		return fmt.Errorf("open %s: %w", backupsRootDir, err)
	}
	defer unix.Close(root)
	if _, err := claimDirFD(root, gid, 0o750); err != nil {
		return fmt.Errorf("%s: %w", backupsRootDir, err)
	}
	names, err := dirNamesFD(root)
	if err != nil {
		return fmt.Errorf("list %s: %w", backupsRootDir, err)
	}
	for _, name := range names {
		if name == restoreStagingName {
			continue
		}
		if err := claimBackupsEntry(root, name); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(backupsRootDir, name), err)
		}
	}
	if err := secureRestoreStaging(root); err != nil {
		return fmt.Errorf("%s: %w", restoreStagingRoot(), err)
	}
	return nil
}

// claimBackupsEntry handles one entry directly under the backups folder that
// may not be root's.
func claimBackupsEntry(root int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(root, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if int(st.Uid) == backupsOwnerUID {
		return nil
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return unix.Unlinkat(root, name, 0)
	case unix.S_IFDIR:
		fd, err := unix.Openat(root, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		_, err = claimDirFD(fd, -1, -1)
		return err
	case unix.S_IFREG:
		// The agent's own lock files are made again on use. Any other file is
		// left where it is: root never reads a file it didn't make there.
		if name == ".restore.lock" || (strings.HasPrefix(name, ".init-") && strings.HasSuffix(name, ".lock")) {
			return unix.Unlinkat(root, name, 0)
		}
	}
	return nil
}

// secureRestoreStaging makes restore-staging a root-only folder. When it wasn't
// root's, every entry in it that isn't root's is removed: those can't be a
// stage the agent made.
func secureRestoreStaging(root int) error {
	var st unix.Stat_t
	err := unix.Fstatat(root, restoreStagingName, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
	case err != nil:
		return err
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		if err := unix.Unlinkat(root, restoreStagingName, 0); err != nil {
			return err
		}
	}
	if err := unix.Mkdirat(root, restoreStagingName, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	fd, err := unix.Openat(root, restoreStagingName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	wasRoots, err := claimDirFD(fd, -1, 0o700)
	if err != nil || wasRoots {
		return err
	}
	names, err := dirNamesFD(fd)
	if err != nil {
		return err
	}
	var stage *os.Root
	for _, name := range names {
		var est unix.Stat_t
		if err := unix.Fstatat(fd, name, &est, unix.AT_SYMLINK_NOFOLLOW); err != nil || int(est.Uid) == backupsOwnerUID {
			continue
		}
		if est.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := unix.Unlinkat(fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
			continue
		}
		if stage == nil {
			if stage, err = os.OpenRoot(restoreStagingRoot()); err != nil {
				return err
			}
			defer stage.Close()
		}
		if err := stage.RemoveAll(name); err != nil {
			return err
		}
	}
	return nil
}

// claimDirFD gives the open folder to root and gid (-1 keeps the group), with
// perm (-1 keeps the mode less group and other write). It reports whether the
// folder was root's already.
func claimDirFD(fd, gid, perm int) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	wasRoots := int(st.Uid) == backupsOwnerUID
	if !wasRoots || (gid >= 0 && int(st.Gid) != gid) {
		if err := backupsFchown(fd, backupsOwnerUID, gid); err != nil {
			return wasRoots, fmt.Errorf("chown: %w", err)
		}
	}
	mode := int(st.Mode & 0o7777)
	want := perm
	if want < 0 {
		want = mode &^ 0o022
	}
	if mode != want {
		if err := unix.Fchmod(fd, uint32(want)); err != nil {
			return wasRoots, fmt.Errorf("chmod: %w", err)
		}
	}
	return wasRoots, nil
}

// openDirRootLinksOnly opens a folder one path component at a time. A link is
// followed only when root owns it and the folder it sits in, and nobody else
// can write that folder: install.sh's /var/lib/jabali-backups link, or one an
// admin made to move the backups to another disk. Any other link fails.
func openDirRootLinksOnly(path string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	comps := strings.Split(path, "/")
	for hops := 0; len(comps) > 0; {
		name := comps[0]
		comps = comps[1:]
		if name == "" || name == "." {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			unix.Close(fd)
			return -1, err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			var parent unix.Stat_t
			if err := unix.Fstat(fd, &parent); err != nil {
				unix.Close(fd)
				return -1, err
			}
			if int(st.Uid) != backupsOwnerUID || int(parent.Uid) != backupsOwnerUID || parent.Mode&0o022 != 0 {
				unix.Close(fd)
				return -1, fmt.Errorf("%s is a link root doesn't own alone; not followed", name)
			}
			if hops++; hops > 8 {
				unix.Close(fd)
				return -1, unix.ELOOP
			}
			buf := make([]byte, unix.PathMax)
			n, err := unix.Readlinkat(fd, name, buf)
			if err != nil {
				unix.Close(fd)
				return -1, err
			}
			target := string(buf[:n])
			if strings.HasPrefix(target, "/") {
				unix.Close(fd)
				if fd, err = unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err != nil {
					return -1, err
				}
			}
			comps = append(strings.Split(target, "/"), comps...)
			continue
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// dirNamesFD lists an open folder through a duplicate of its handle.
func dirNamesFD(fd int) ([]string, error) {
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "")
	defer f.Close()
	return f.Readdirnames(-1)
}
