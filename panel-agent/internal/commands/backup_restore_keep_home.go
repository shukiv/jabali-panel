package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// backup_restore_keep_home.go — GH #1993. A restore that keeps what is
// already on the server ("Overwrite existing items with the backup" off)
// adds to the account's home only the files it doesn't have yet.
//
// SECURITY: root does the copy, into a tree the tenant owns. A plain copy
// (rsync --ignore-existing, tar --skip-old-files) skips a tenant's symlink
// as "already there" and then writes the backup's directory under it — as
// root, through the link, to wherever it points. So the copy here never
// follows a link in the home: it opens each directory with openat
// O_NOFOLLOW from the one above, starting at the home, and creates each
// entry with O_EXCL (O_NOFOLLOW) or mkdirat/symlinkat, relative to that
// verified directory fd. Anything the tenant has at a name, link or not, is
// "already there" and is left alone. The staged tree it reads from is
// root's, so it is read by path.

const keepHomeDirFlags = unix.O_NOFOLLOW | unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC

// keepHomeRace, when set, runs between looking at a home entry and opening
// or creating it. Tests plant or swap the entry there, as a racing tenant
// would.
var keepHomeRace func(name string)

func raceWindow(name string) {
	if keepHomeRace != nil {
		keepHomeRace(name)
	}
}

// addMissingHomeFiles copies into the home dst the entries of the staged
// tree src it doesn't have, owned uid:gid with the backup's modes and
// times. Existing entries are kept as they are. Only directories, regular
// files and symlinks are copied (a symlink as a link, never followed); hard
// links are copied as separate files.
func addMissingHomeFiles(ctx context.Context, src, dst string, uid, gid int) error {
	src, home := filepath.Clean(src), filepath.Clean(dst)
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("read the backup's files: %w", err)
	}
	if _, err := os.Lstat(home); errors.Is(err, fs.ErrNotExist) {
		// A home the restore recreated (useradd --no-create-home). Its
		// parent (/home) is root's, so a path is safe here.
		if err := os.Mkdir(home, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", home, err)
		}
		if err := os.Lchown(home, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", home, err)
		}
		if err := os.Chmod(home, srcInfo.Mode().Perm()); err != nil {
			return fmt.Errorf("chmod %s: %w", home, err)
		}
	}
	hfd, err := unix.Open(home, keepHomeDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", home, err)
	}
	defer unix.Close(hfd)
	c := keepHomeCopy{ctx: ctx, uid: uid, gid: gid}
	c.dir(src, hfd, "")
	if c.failed > 0 {
		return fmt.Errorf("%d files not added: %s", c.failed, strings.Join(c.errs, "; "))
	}
	return nil
}

type keepHomeCopy struct {
	ctx      context.Context
	uid, gid int
	failed   int
	errs     []string // the first few failures
}

func (c *keepHomeCopy) fail(rel string, err error) {
	c.failed++
	if len(c.errs) < 3 {
		c.errs = append(c.errs, fmt.Sprintf("%s: %v", rel, err))
	}
}

// dir copies the missing entries of the staged directory src into the home
// directory dfd (rel is its path in the home, for messages).
func (c *keepHomeCopy) dir(src string, dfd int, rel string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		c.fail(rel, err)
		return
	}
	for _, e := range entries {
		if c.ctx.Err() != nil {
			c.fail(rel, c.ctx.Err())
			return
		}
		name, srcPath, entRel := e.Name(), filepath.Join(src, e.Name()), filepath.Join(rel, e.Name())
		info, err := os.Lstat(srcPath)
		if err != nil {
			c.fail(entRel, err)
			continue
		}
		var st unix.Stat_t
		statErr := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case statErr == nil:
			// Already there: keep it. Descend only into a real directory.
			if info.IsDir() && st.Mode&unix.S_IFMT == unix.S_IFDIR {
				c.descend(srcPath, info, dfd, name, entRel, false)
			}
		case !errors.Is(statErr, unix.ENOENT):
			c.fail(entRel, statErr)
		case info.IsDir():
			raceWindow(name)
			if err := unix.Mkdirat(dfd, name, 0o700); err != nil {
				if !errors.Is(err, unix.EEXIST) { // raced: something is there now
					c.fail(entRel, err)
				}
				continue
			}
			c.descend(srcPath, info, dfd, name, entRel, true)
		case info.Mode().IsRegular():
			c.file(srcPath, info, dfd, name, entRel)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err == nil {
				raceWindow(name)
				err = unix.Symlinkat(target, dfd, name)
			}
			if err == nil {
				err = unix.Fchownat(dfd, name, c.uid, c.gid, unix.AT_SYMLINK_NOFOLLOW)
			}
			if err != nil && !errors.Is(err, unix.EEXIST) {
				c.fail(entRel, err)
			}
		}
		// Anything else (device, fifo, socket) isn't copied.
	}
}

// descend opens the home directory name under dfd without following a link
// and copies into it. A directory the copy created gets the staged one's
// owner, mode and times after its contents (so its times stay the backup's);
// one that was already there keeps its own.
func (c *keepHomeCopy) descend(srcPath string, info fs.FileInfo, dfd int, name, rel string, created bool) {
	raceWindow(name)
	cfd, err := unix.Openat(dfd, name, keepHomeDirFlags, 0)
	if err != nil {
		return // no longer a directory: what is there now is kept
	}
	defer unix.Close(cfd)
	c.dir(srcPath, cfd, rel)
	if created {
		if err := c.finish(cfd, info, true); err != nil {
			c.fail(rel, err)
		}
	}
}

// file creates the regular file name under dfd (O_EXCL, O_NOFOLLOW) with the
// staged file's content.
func (c *keepHomeCopy) file(srcPath string, info fs.FileInfo, dfd int, name, rel string) {
	raceWindow(name)
	fd, err := unix.Openat(dfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if !errors.Is(err, unix.EEXIST) {
			c.fail(rel, err)
		}
		return
	}
	out := os.NewFile(uintptr(fd), name)
	defer out.Close()
	in, err := os.Open(srcPath)
	if err == nil {
		_, err = io.Copy(out, in)
		in.Close()
	}
	if err == nil {
		err = c.finish(fd, info, false)
	}
	if err != nil {
		c.fail(rel, err)
	}
}

// finish gives the entry at fd the account's owner and the staged entry's
// mode and times. Set-user-ID never carries over, nor set-group-ID on a file
// (a chown drops both on Linux, as the replacing restore's chown does).
func (c *keepHomeCopy) finish(fd int, info fs.FileInfo, isDir bool) error {
	if err := unix.Fchown(fd, c.uid, c.gid); err != nil {
		return fmt.Errorf("fchown: %w", err)
	}
	mode := uint32(info.Mode().Perm())
	if isDir && info.Mode()&fs.ModeSetgid != 0 {
		mode |= unix.S_ISGID
	}
	if info.Mode()&fs.ModeSticky != 0 {
		mode |= unix.S_ISVTX
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return fmt.Errorf("fchmod: %w", err)
	}
	mt := unix.NsecToTimeval(info.ModTime().UnixNano())
	if err := unix.Futimes(fd, []unix.Timeval{mt, mt}); err != nil {
		return fmt.Errorf("set times: %w", err)
	}
	return nil
}
