// openat2.go — kernel-enforced, escape-proof path operations for the file
// manager. This is the fix for the parent-directory symlink-escape and TOCTOU
// classes (GH #421/#422/#423/#424/#427/#428): the logical Clean()/EvalSymlinks
// + O_NOFOLLOW approach cannot prevent a tenant-planted symlink in a PARENT
// directory from redirecting a root-side op, and re-stat-after-resolve cannot
// close a TOCTOU. openat2(2) resolves the whole path atomically in the kernel
// under containment flags, so neither hole exists. Every act (open/create/
// rename/unlink/chmod) is performed relative to an escape-proof fd, never by
// handing a resolved string back to an os.* call that would re-resolve it.
//
// All operations resolve relative to a base-dir fd (an owned docroot) under:
//
//	RESOLVE_BENEATH — every component must stay beneath the base; ANY absolute
//	                  symlink and any ".." that would climb above the base are
//	                  rejected by the kernel mid-walk. Only relative symlinks
//	                  that stay within the base resolve.
//
// Why RESOLVE_BENEATH and NOT also RESOLVE_NO_SYMLINKS: every documented escape
// (#421/#422/#423/#427) plants an ABSOLUTE symlink (e.g. ~/x -> /etc/cron.d) or
// an upward ".." link — both of which RESOLVE_BENEATH blocks atomically in the
// kernel. Adding RESOLVE_NO_SYMLINKS would also break legitimate in-scope
// symlinks the panel itself relies on (e.g. ~/.jabali/bin/php) and tenant
// deploy-style relative links (current -> releases/v2), with no extra security:
// a single owned docroot is one trust domain, so a relative link that stays
// beneath it crosses no boundary. The base dir itself is opened O_NOFOLLOW so
// it can never be a symlink.
//
// Kernel requirement: openat2(2) is Linux 5.6+. On older kernels Openat2
// returns ENOSYS; we FAIL CLOSED (return the error) rather than fall back to
// the unsafe logical path — breaking the file manager is preferable to
// reopening the escape.
package filesafe

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// resolveFlags is the escape-proof containment applied to every resolution.
// See the package-level note for why RESOLVE_BENEATH alone (not also
// RESOLVE_NO_SYMLINKS) is the correct choice for a single-tenant docroot.
const resolveFlags = unix.RESOLVE_BENEATH

// baseFor returns the owned docroot that contains pathStr and the path relative
// to that docroot (the argument handed to openat2 against the base-dir fd).
// It runs the same string validation as Clean/Resolve (NUL, control, "..",
// length, absolute) before matching a docroot.
func (s *Scope) baseFor(pathStr string) (base, rel string, err error) {
	if err = s.validatePathString(pathStr); err != nil {
		return "", "", err
	}
	cleaned := filepath.Clean(pathStr)
	if !filepath.IsAbs(cleaned) {
		return "", "", &ValidationError{Code: ErrCodeNotAbsolute, Detail: fmt.Sprintf("path %q is not absolute", pathStr)}
	}
	for _, dr := range s.OwnedDocroots {
		dr = filepath.Clean(dr)
		if cleaned == dr {
			return dr, ".", nil
		}
		if strings.HasPrefix(cleaned, dr+"/") {
			return dr, cleaned[len(dr)+1:], nil
		}
	}
	return "", "", &ValidationError{Code: ErrCodeNotInScope, Detail: fmt.Sprintf("path %q is not within owned docroots: %v", pathStr, s.OwnedDocroots)}
}

// openBase opens the (root-owned, agent-managed) docroot dir itself with
// O_NOFOLLOW so the base can't be a symlink. Every component BELOW it is then
// resolved by the kernel under resolveFlags.
func openBase(base string) (*os.File, error) {
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), base), nil
}

// openAt2 resolves rel beneath baseFd under resolveFlags and returns an *os.File.
func openAt2(baseFd *os.File, rel string, flags int, mode uint32) (*os.File, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Mode:    uint64(mode),
		Resolve: resolveFlags,
	}
	fd, err := unix.Openat2(int(baseFd.Fd()), rel, how)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), rel), nil
}

// OpenInScope opens a file safely within scope: no parent symlink can redirect
// the op and resolution cannot escape the owning docroot. Use for read and for
// append/create where the leaf must not be redirected out of scope.
func (s *Scope) OpenInScope(pathStr string, flags int, perm os.FileMode) (*os.File, error) {
	base, rel, err := s.baseFor(pathStr)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, &ValidationError{Code: ErrCodeNotInScope, Detail: "refusing to open docroot itself as a file"}
	}
	bf, err := openBase(base)
	if err != nil {
		return nil, err
	}
	defer bf.Close()
	return openAt2(bf, rel, flags, uint32(perm.Perm()))
}

// OpenDirInScope opens a directory (O_DIRECTORY|O_RDONLY) escape-proof. Unlike
// OpenInScope it allows the docroot root itself (rel == ".") so the file
// manager can list/du/stat the user's home. The returned *os.File MUST be
// Closed by the caller.
func (s *Scope) OpenDirInScope(pathStr string) (*os.File, error) {
	base, rel, err := s.baseFor(pathStr)
	if err != nil {
		return nil, err
	}
	bf, err := openBase(base)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return bf, nil // base IS the requested dir; hand it back
	}
	defer bf.Close()
	return openAt2(bf, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
}

// OpenParentInScope opens the parent directory of pathStr (escape-proof) and
// returns it plus the validated leaf basename. Callers perform *at(2) ops
// (openat/renameat/unlinkat/fstatat) relative to the returned fd, so the final
// component is acted on with no re-resolution and no parent-symlink follow. The
// returned *os.File MUST be Closed by the caller.
func (s *Scope) OpenParentInScope(pathStr string) (parent *os.File, leaf string, err error) {
	base, rel, err := s.baseFor(pathStr)
	if err != nil {
		return nil, "", err
	}
	if rel == "." {
		return nil, "", &ValidationError{Code: ErrCodeNotInScope, Detail: "path has no parent within scope"}
	}
	dir, leaf := filepath.Split(rel)
	leaf = strings.TrimSuffix(leaf, "/")
	if leaf == "" || leaf == "." || leaf == ".." || strings.ContainsAny(leaf, "/") {
		return nil, "", &ValidationError{Code: ErrCodeTraversal, Detail: fmt.Sprintf("invalid leaf %q", leaf)}
	}
	bf, err := openBase(base)
	if err != nil {
		return nil, "", err
	}
	defer bf.Close()

	relDir := strings.TrimSuffix(dir, "/")
	if relDir == "" {
		// Parent IS the base docroot — hand back a dup of the base fd.
		dupFd, dErr := unix.Dup(int(bf.Fd()))
		if dErr != nil {
			return nil, "", dErr
		}
		return os.NewFile(uintptr(dupFd), base), leaf, nil
	}
	parent, err = openAt2(bf, relDir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, "", err
	}
	return parent, leaf, nil
}

// LeafInfo is the escape-proof lstat result for a single path or directory
// entry: metadata of the leaf itself, never of a symlink's target.
type LeafInfo struct {
	Name      string
	Size      int64
	Mode      os.FileMode
	ModTime   time.Time
	IsDir     bool
	IsSymlink bool
}

// statToLeaf converts a raw unix.Stat_t (from fstatat AT_SYMLINK_NOFOLLOW) into
// a LeafInfo with a faithful os.FileMode (type + perm + special bits).
func statToLeaf(name string, st *unix.Stat_t) LeafInfo {
	m := os.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m |= os.ModeDir
	case unix.S_IFLNK:
		m |= os.ModeSymlink
	case unix.S_IFCHR:
		m |= os.ModeDevice | os.ModeCharDevice
	case unix.S_IFBLK:
		m |= os.ModeDevice
	case unix.S_IFIFO:
		m |= os.ModeNamedPipe
	case unix.S_IFSOCK:
		m |= os.ModeSocket
	}
	if st.Mode&unix.S_ISUID != 0 {
		m |= os.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		m |= os.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		m |= os.ModeSticky
	}
	return LeafInfo{
		Name:      name,
		Size:      st.Size,
		Mode:      m,
		ModTime:   time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
		IsDir:     st.Mode&unix.S_IFMT == unix.S_IFDIR,
		IsSymlink: st.Mode&unix.S_IFMT == unix.S_IFLNK,
	}
}

// StatInScope lstat-equivalents pathStr escape-proof: it reports the leaf's own
// metadata (a final symlink is described as a symlink, never followed). For the
// docroot root itself it stats the base dir.
func (s *Scope) StatInScope(pathStr string) (*LeafInfo, error) {
	base, rel, err := s.baseFor(pathStr)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		bf, err := openBase(base)
		if err != nil {
			return nil, err
		}
		defer bf.Close()
		var st unix.Stat_t
		if err := unix.Fstat(int(bf.Fd()), &st); err != nil {
			return nil, err
		}
		li := statToLeaf(filepath.Base(base), &st)
		return &li, nil
	}
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	li := statToLeaf(leaf, &st)
	return &li, nil
}

// ExistsInScope reports whether pathStr's leaf exists (without following a leaf
// symlink) and, if so, its LeafInfo. A non-existent leaf returns (nil, nil);
// any other error is returned. Use for overwrite / collision checks that must
// be made through the SAME escape-proof parent the act will use.
func (s *Scope) ExistsInScope(pathStr string) (*LeafInfo, error) {
	li, err := s.StatInScope(pathStr)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return li, nil
}

// ReadDirInScope lists pathStr's directory entries escape-proof: the directory
// is opened under RESOLVE_BENEATH and every entry is fstatat'd against that fd
// with AT_SYMLINK_NOFOLLOW, so neither the directory nor any entry can be
// redirected by a planted symlink. Entries are returned sorted by name.
// maxListEntries bounds a single directory listing so a tenant-created
// high-fanout directory can't force the root agent to allocate an unbounded
// []LeafInfo response (GH #659).
const maxListEntries = 100000

func (s *Scope) ReadDirInScope(pathStr string) ([]LeafInfo, error) {
	dir, err := s.OpenDirInScope(pathStr)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(maxListEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(names) > maxListEntries {
		return nil, fmt.Errorf("directory has more than %d entries; narrow the path", maxListEntries)
	}
	sort.Strings(names)
	out := make([]LeafInfo, 0, len(names))
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // raced unlink / unreadable — skip, same as os.ReadDir loop
		}
		out = append(out, statToLeaf(name, &st))
	}
	return out, nil
}

// CreateExclInScope creates a new file (O_CREAT|O_EXCL|O_NOFOLLOW) relative to
// its escape-proof parent fd. Returns the open *os.File for the caller to
// write/Fchown/Fchmod/Close. Fails (EEXIST) if the leaf already exists.
func (s *Scope) CreateExclInScope(pathStr string, perm os.FileMode) (*os.File, error) {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), leaf,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), pathStr), nil
}

// RenameInScope renames oldPath→newPath, both resolved escape-proof, via
// renameat(2) against their respective parent fds.
func (s *Scope) RenameInScope(oldPath, newPath string) error {
	op, ol, err := s.OpenParentInScope(oldPath)
	if err != nil {
		return err
	}
	defer op.Close()
	np, nl, err := s.OpenParentInScope(newPath)
	if err != nil {
		return err
	}
	defer np.Close()
	return unix.Renameat(int(op.Fd()), ol, int(np.Fd()), nl)
}

// RenameIntoInScope renames an OUT-OF-SCOPE source (e.g. a root-owned upload
// staging file under a fixed prefix the caller has already validated) into an
// in-scope destination, via renameat(AT_FDCWD, srcAbs, destParentFd, leaf). The
// destination parent is escape-proof; the source is taken verbatim (the caller
// owns its trust). No-clobber is NOT enforced here — callers that need it must
// ExistsInScope first.
func (s *Scope) RenameIntoInScope(srcAbs, destPath string) error {
	np, nl, err := s.OpenParentInScope(destPath)
	if err != nil {
		return err
	}
	defer np.Close()
	return unix.Renameat(unix.AT_FDCWD, srcAbs, int(np.Fd()), nl)
}

// RemoveInScope unlinks the leaf (file: flag 0; dir: AT_REMOVEDIR) via
// unlinkat(2) against its escape-proof parent fd.
func (s *Scope) RemoveInScope(pathStr string, isDir bool) error {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		return err
	}
	defer parent.Close()
	flag := 0
	if isDir {
		flag = unix.AT_REMOVEDIR
	}
	return unix.Unlinkat(int(parent.Fd()), leaf, flag)
}

// RemoveAllInScope removes pathStr recursively, escape-proof: it opens the leaf's
// parent fd, then descends with openat(O_DIRECTORY|O_NOFOLLOW) + unlinkat per
// level so no component is ever re-resolved through a swapped parent symlink
// (the os.RemoveAll(string) hole — #428). A leaf symlink is unlinked, never
// followed. Idempotent: a missing leaf is success.
func (s *Scope) RemoveAllInScope(ctx context.Context, pathStr string) error {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		return err
	}
	defer parent.Close()
	return removeAllAt(ctx, int(parent.Fd()), leaf)
}

// removeAllAt removes name (file, symlink, or directory subtree) relative to
// dirFd. Directories are descended via an O_NOFOLLOW dir fd so a symlink can
// never redirect the recursion.
func removeAllAt(ctx context.Context, dirFd int, name string) error {
	// GH #664: honor cancellation before each unlink so a canceled delete stops.
	if err := ctx.Err(); err != nil {
		return err
	}
	// Fast path: try a plain unlink (handles files and symlinks).
	err := unix.Unlinkat(dirFd, name, 0)
	if err == nil || err == unix.ENOENT {
		return nil
	}
	if err != unix.EISDIR && err != unix.EPERM && err != unix.ENOTEMPTY {
		return err
	}
	// It's a directory: open it O_NOFOLLOW and recurse.
	cfd, oerr := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if oerr != nil {
		if oerr == unix.ENOENT {
			return nil
		}
		// A symlink leaf would ELOOP/ENOTDIR here — unlink it as a plain file.
		if oerr == unix.ELOOP || oerr == unix.ENOTDIR {
			if uerr := unix.Unlinkat(dirFd, name, 0); uerr != nil && uerr != unix.ENOENT {
				return uerr
			}
			return nil
		}
		return oerr
	}
	child := os.NewFile(uintptr(cfd), name)
	names, rerr := child.Readdirnames(-1)
	if rerr != nil {
		child.Close()
		return rerr
	}
	for _, n := range names {
		if rmErr := removeAllAt(ctx, cfd, n); rmErr != nil {
			child.Close()
			return rmErr
		}
	}
	child.Close()
	return unix.Unlinkat(dirFd, name, unix.AT_REMOVEDIR)
}

// CopyTreeInScope copies srcPath→dstPath, BOTH escape-proof, via fd descent.
// dst must not already exist. Directories are recreated and descended via
// O_NOFOLLOW dir fds on both sides, so a tenant cannot plant a symlink inside
// the partially-built copy to redirect a later write (the os-walk hole — #422).
// Regular-file content is copied; symlinks are reproduced as symlinks (their
// target string is data, not followed). Each created file/dir is chowned to
// uid:gid. Returns total bytes of regular-file content copied.
func (s *Scope) CopyTreeInScope(ctx context.Context, srcPath, dstPath string, uid, gid int) (int64, error) {
	srcInfo, err := s.StatInScope(srcPath)
	if err != nil {
		return 0, err
	}
	dstParent, dstLeaf, err := s.OpenParentInScope(dstPath)
	if err != nil {
		return 0, err
	}
	defer dstParent.Close()

	switch {
	case srcInfo.IsSymlink:
		sp, sl, err := s.OpenParentInScope(srcPath)
		if err != nil {
			return 0, err
		}
		target, rerr := readlinkat(int(sp.Fd()), sl)
		sp.Close()
		if rerr != nil {
			return 0, rerr
		}
		if err := unix.Symlinkat(target, int(dstParent.Fd()), dstLeaf); err != nil {
			return 0, err
		}
		// GH #662: chown the symlink itself fatal too — every root-created object
		// in a tenant tree must be owned by the tenant or the copy fails.
		if chErr := unix.Fchownat(int(dstParent.Fd()), dstLeaf, uid, gid, unix.AT_SYMLINK_NOFOLLOW); chErr != nil {
			return 0, fmt.Errorf("chown copied symlink %q: %w", dstLeaf, chErr)
		}
		return 0, nil

	case srcInfo.IsDir:
		sfd, err := s.OpenDirInScope(srcPath)
		if err != nil {
			return 0, err
		}
		defer sfd.Close()
		if err := unix.Mkdirat(int(dstParent.Fd()), dstLeaf, uint32(srcInfo.Mode.Perm())); err != nil {
			return 0, err
		}
		dfd, err := unix.Openat(int(dstParent.Fd()), dstLeaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return 0, err
		}
		if chErr := unix.Fchownat(dfd, "", uid, gid, unix.AT_EMPTY_PATH); chErr != nil {
			unix.Close(dfd)
			return 0, fmt.Errorf("chown copied dir: %w", chErr) // GH #662
		}
		n, cerr := copyDirContents(ctx, int(sfd.Fd()), dfd, uid, gid)
		unix.Close(dfd)
		return n, cerr

	case srcInfo.Mode.IsRegular():
		in, err := s.OpenInScope(srcPath, os.O_RDONLY, 0)
		if err != nil {
			return 0, err
		}
		defer in.Close()
		ofd, err := unix.Openat(int(dstParent.Fd()), dstLeaf, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(srcInfo.Mode.Perm()))
		if err != nil {
			return 0, err
		}
		out := os.NewFile(uintptr(ofd), dstLeaf)
		// GH #663: chown BEFORE writing so the target user's quota is enforced
		// during the copy (root has no quota). GH #662: fatal on chown failure.
		if chErr := out.Chown(uid, gid); chErr != nil {
			_ = out.Close()
			return 0, fmt.Errorf("chown copied file %q: %w", dstLeaf, chErr)
		}
		n, cerr := io.Copy(out, in)
		if closeErr := out.Close(); cerr == nil {
			cerr = closeErr
		}
		return n, cerr

	default:
		return 0, nil // devices/sockets/fifos: nothing to copy
	}
}

// copyDirContents copies every entry under srcFd into dstFd (both open dir fds),
// recursing with O_NOFOLLOW so neither side can be redirected mid-walk.
func copyDirContents(ctx context.Context, srcFd, dstFd, uid, gid int) (int64, error) {
	dup, err := unix.Dup(srcFd)
	if err != nil {
		return 0, err
	}
	sdir := os.NewFile(uintptr(dup), ".")
	names, err := sdir.Readdirnames(-1)
	sdir.Close()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, name := range names {
		// GH #664: honor cancellation between entries so a canceled copy stops.
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var st unix.Stat_t
		if err := unix.Fstatat(srcFd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		li := statToLeaf(name, &st)
		n, err := copyEntryAt(ctx, srcFd, dstFd, name, li, uid, gid)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// copyEntryAt copies a single entry `name` from srcFd to dstFd.
func copyEntryAt(ctx context.Context, srcFd, dstFd int, name string, li LeafInfo, uid, gid int) (int64, error) {
	switch {
	case li.IsSymlink:
		target, err := readlinkat(srcFd, name)
		if err != nil {
			return 0, err
		}
		if err := unix.Symlinkat(target, dstFd, name); err != nil {
			return 0, err
		}
		if chErr := unix.Fchownat(dstFd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); chErr != nil { // GH #662
			return 0, fmt.Errorf("chown copied symlink %q: %w", name, chErr)
		}
		return 0, nil
	case li.IsDir:
		if err := unix.Mkdirat(dstFd, name, uint32(li.Mode.Perm())); err != nil {
			return 0, err
		}
		ndst, err := unix.Openat(dstFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return 0, err
		}
		// GH #662: recursive subdir chown is fatal too — a root-owned dir inside
		// a tenant tree bypasses quota accounting, same as a top-level dir.
		if chErr := unix.Fchownat(ndst, "", uid, gid, unix.AT_EMPTY_PATH); chErr != nil {
			unix.Close(ndst)
			return 0, fmt.Errorf("chown copied dir %q: %w", name, chErr)
		}
		nsrc, err := unix.Openat(srcFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			unix.Close(ndst)
			return 0, err
		}
		n, cerr := copyDirContents(ctx, nsrc, ndst, uid, gid)
		unix.Close(nsrc)
		unix.Close(ndst)
		return n, cerr
	case li.Mode.IsRegular():
		return copyRegularAt(srcFd, dstFd, name, li.Mode.Perm(), uid, gid)
	default:
		return 0, nil // skip devices/sockets/fifos
	}
}

// copyRegularAt copies a regular file `name` from srcFd to dstFd via openat.
func copyRegularAt(srcFd, dstFd int, name string, perm os.FileMode, uid, gid int) (int64, error) {
	ifd, err := unix.Openat(srcFd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	in := os.NewFile(uintptr(ifd), name)
	defer in.Close()
	ofd, err := unix.Openat(dstFd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm))
	if err != nil {
		return 0, err
	}
	out := os.NewFile(uintptr(ofd), name)
	// GH #663 + #662: chown before writing (quota) + fatal on failure.
	if chErr := out.Chown(uid, gid); chErr != nil {
		_ = out.Close()
		return 0, fmt.Errorf("chown copied file %q: %w", name, chErr)
	}
	n, cerr := io.Copy(out, in)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	return n, cerr
}

// ReadlinkInScope reads the symlink target at pathStr fd-safely: the parent is
// opened escape-proof and readlinkat reads the leaf without following it, so no
// traversal can escape the scope (GH #650, used by the fd-safe archive walk).
func (s *Scope) ReadlinkInScope(pathStr string) (string, error) {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	return readlinkat(int(parent.Fd()), leaf)
}

// readlinkat reads a symlink target relative to dirFd without following it.
func readlinkat(dirFd int, name string) (string, error) {
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(dirFd, name, buf)
	if err != nil {
		return "", err
	}
	if n >= len(buf) {
		return "", fmt.Errorf("symlink target too long: %q", name)
	}
	return string(buf[:n]), nil
}

// ChmodInScope changes mode bits without following a final-component symlink:
// it opens the leaf O_NOFOLLOW (ELOOP ⇒ refuse, it's a symlink) then Fchmods
// the fd. Special bits (setuid/setgid/sticky) are the caller's responsibility
// to mask (see GH #429).
func (s *Scope) ChmodInScope(pathStr string, mode os.FileMode) error {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		return err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ELOOP {
		return &ValidationError{Code: ErrCodeSymlinkEscape, Detail: "refusing to chmod a symlink"}
	}
	if err != nil {
		// Write-only / special files: reopen via O_PATH and chmod through
		// the proc magic-symlink (still anchored to the O_NOFOLLOW fd).
		pfd, perr := unix.Openat(int(parent.Fd()), leaf, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if perr == unix.ELOOP {
			return &ValidationError{Code: ErrCodeSymlinkEscape, Detail: "refusing to chmod a symlink"}
		}
		if perr != nil {
			return perr
		}
		defer unix.Close(pfd)
		return unix.Fchmodat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", pfd), uint32(mode), 0)
	}
	defer unix.Close(fd)
	return unix.Fchmod(fd, uint32(mode))
}

// MkdirInScope creates a directory escape-proof. With parents=false it mkdirats
// the single leaf against its openat2 parent fd. With parents=true it first
// builds any missing intermediate components via openat(O_NOFOLLOW) descent
// from the base, so no component is ever created through a swapped parent
// symlink. Returns whether a NEW leaf was created (false if it already existed,
// which is not an error — idempotent like os.Mkdir's EEXIST handling here).
func (s *Scope) MkdirInScope(pathStr string, parents bool, perm os.FileMode) (bool, error) {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		if !parents {
			return false, err
		}
		// Parent chain may be missing — build it, then retry once.
		if merr := s.mkdirParents(pathStr, perm); merr != nil {
			return false, merr
		}
		parent, leaf, err = s.OpenParentInScope(pathStr)
		if err != nil {
			return false, err
		}
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), leaf, uint32(perm.Perm())); err != nil {
		if err == unix.EEXIST {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// mkdirParents creates every missing PARENT component of pathStr (not the leaf)
// by descending from the base docroot fd one component at a time with
// O_NOFOLLOW, so an existing symlink component is refused (ELOOP) rather than
// followed. Components are taken from the validated relative path (no "..").
func (s *Scope) mkdirParents(pathStr string, perm os.FileMode) error {
	base, rel, err := s.baseFor(pathStr)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	bf, err := openBase(base)
	if err != nil {
		return err
	}
	defer bf.Close()
	cur := int(bf.Fd())
	var toClose []int
	defer func() {
		for _, fd := range toClose {
			unix.Close(fd)
		}
	}()
	for _, c := range strings.Split(filepath.Dir(rel), "/") {
		if c == "" || c == "." {
			continue
		}
		if err := unix.Mkdirat(cur, c, uint32(perm.Perm())); err != nil && err != unix.EEXIST {
			return err
		}
		nfd, err := unix.Openat(cur, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		toClose = append(toClose, nfd)
		cur = nfd
	}
	return nil
}

// ExtractDir is an escape-proof destination for archive extraction: a base
// directory fd plus the uid/gid that created entries are chowned to. Paths are
// slash-relative under the base; every component is created/opened via
// openat(O_NOFOLLOW), so a pre-planted parent-directory symlink in the
// (tenant-owned) destination tree can never redirect a write outside the base —
// the extract WRITE side of Gitea #423/#424. Close() releases the base fd.
type ExtractDir struct {
	base     *os.File
	uid, gid int
}

// NewExtractDir opens destPath escape-proof as the extraction base.
func (s *Scope) NewExtractDir(destPath string, uid, gid int) (*ExtractDir, error) {
	base, err := s.OpenDirInScope(destPath)
	if err != nil {
		return nil, err
	}
	return &ExtractDir{base: base, uid: uid, gid: gid}, nil
}

// Close releases the base dir fd.
func (e *ExtractDir) Close() error { return e.base.Close() }

// splitExtractRel cleans a slash-relative archive path into components, rejecting
// absolute paths, "", and any ".." escape.
func splitExtractRel(rel string) ([]string, error) {
	rel = strings.TrimPrefix(rel, "/")
	out := make([]string, 0, 8)
	for _, c := range strings.Split(rel, "/") {
		if c == "" || c == "." {
			continue
		}
		if c == ".." || strings.ContainsRune(c, 0) {
			return nil, &ValidationError{Code: ErrCodeTraversal, Detail: fmt.Sprintf("extract path escapes destination: %q", rel)}
		}
		out = append(out, c)
	}
	return out, nil
}

// descend opens each component of comps under the base. When mkMode != 0 missing
// components are created (mkdirat) and newly-created dirs chowned to uid:gid.
// Every open is O_NOFOLLOW|O_DIRECTORY, so a symlink component is refused
// (ELOOP). Returns the deepest dir fd; the caller closes it unless isBase.
func (e *ExtractDir) descend(comps []string, mkMode os.FileMode) (fd int, isBase bool, err error) {
	cur := int(e.base.Fd())
	curIsBase := true
	for _, c := range comps {
		created := false
		if mkMode != 0 {
			merr := unix.Mkdirat(cur, c, uint32(mkMode.Perm()))
			if merr != nil && merr != unix.EEXIST {
				if !curIsBase {
					unix.Close(cur)
				}
				return -1, false, merr
			}
			created = merr == nil
		}
		nfd, oerr := unix.Openat(cur, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if oerr != nil {
			if !curIsBase {
				unix.Close(cur)
			}
			return -1, false, oerr
		}
		if created {
			if cerr := unix.Fchown(nfd, e.uid, e.gid); cerr != nil {
				unix.Close(nfd)
				if !curIsBase {
					unix.Close(cur)
				}
				return -1, false, fmt.Errorf("chown extract dir: %w", cerr) // GH #662
			}
		}
		if !curIsBase {
			unix.Close(cur)
		}
		cur = nfd
		curIsBase = false
	}
	return cur, curIsBase, nil
}

// MkdirAll creates relDir (slash-relative) under the base, each component
// escape-proof; newly-created dirs are chowned to uid:gid.
func (e *ExtractDir) MkdirAll(relDir string, mode os.FileMode) error {
	if mode == 0 || mode&0o700 == 0 {
		mode = 0o755
	}
	comps, err := splitExtractRel(relDir)
	if err != nil {
		return err
	}
	fd, isBase, err := e.descend(comps, mode)
	if err != nil {
		return err
	}
	if !isBase {
		unix.Close(fd)
	}
	return nil
}

// Create opens relPath's leaf for writing (O_CREAT|O_TRUNC|O_NOFOLLOW) under the
// base, creating intermediate dirs; the leaf is chowned to uid:gid. A symlink at
// the leaf OR at any parent component is refused (ELOOP). Caller streams+Closes.
func (e *ExtractDir) Create(relPath string, perm os.FileMode) (*os.File, error) {
	comps, err := splitExtractRel(relPath)
	if err != nil {
		return nil, err
	}
	if len(comps) == 0 {
		return nil, &ValidationError{Code: ErrCodeTraversal, Detail: "empty extract path"}
	}
	if perm == 0 {
		perm = 0o644
	}
	leaf := comps[len(comps)-1]
	parentFd, isBase, err := e.descend(comps[:len(comps)-1], 0o755)
	if err != nil {
		return nil, err
	}
	ofd, oerr := unix.Openat(parentFd, leaf, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if !isBase {
		unix.Close(parentFd)
	}
	if oerr != nil {
		return nil, oerr
	}
	f := os.NewFile(uintptr(ofd), leaf)
	// GH #662: chown MUST succeed — a root-owned file in the tenant tree is a
	// quota + access bug. Fail closed (caller removes the partial file).
	if cerr := f.Chown(e.uid, e.gid); cerr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("chown extracted file %q: %w", leaf, cerr)
	}
	return f, nil
}

// Remove unlinks relPath's leaf under the base (escape-proof) — used to drop a
// partially-extracted file that breached the per-file size cap.
func (e *ExtractDir) Remove(relPath string) error {
	comps, err := splitExtractRel(relPath)
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return nil
	}
	leaf := comps[len(comps)-1]
	parentFd, isBase, err := e.descend(comps[:len(comps)-1], 0)
	if err != nil {
		return err
	}
	uerr := unix.Unlinkat(parentFd, leaf, 0)
	if !isBase {
		unix.Close(parentFd)
	}
	return uerr
}

// MkdirChownInScope creates the leaf directory at pathStr and chowns it to
// uid:gid, closing the create→chown TOCTOU that a pathname-based
// os.MkdirAll+os.Chown leaves open in a tenant-writable tree (JAB-251).
//
// The parent is opened via openat2(RESOLVE_BENEATH) — any absolute or
// upward-".." symlink component is refused atomically in the kernel. The
// leaf is mkdirat'd against that HELD parent fd, then re-opened
// O_NOFOLLOW|O_DIRECTORY from the SAME fd: a leaf swapped for a symlink
// between the mkdir and the chown is refused with ELOOP rather than
// followed. Fchown then runs on that descriptor, so no pathname is ever
// re-resolved after the security decision. With parents=true the missing
// intermediate components are built first via mkdirParents (O_NOFOLLOW
// descent); only the leaf is chowned, matching os.MkdirAll+os.Chown
// semantics. Idempotent: an existing leaf directory is chowned, not an
// error. The caller must ensure pathStr != the docroot root itself
// (which has no in-scope parent).
func (s *Scope) MkdirChownInScope(pathStr string, perm os.FileMode, parents bool, uid, gid int) error {
	parent, leaf, err := s.OpenParentInScope(pathStr)
	if err != nil {
		if !parents {
			return err
		}
		if merr := s.mkdirParents(pathStr, perm); merr != nil {
			return merr
		}
		parent, leaf, err = s.OpenParentInScope(pathStr)
		if err != nil {
			return err
		}
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), leaf, uint32(perm.Perm())); err != nil && err != unix.EEXIST {
		return err
	}
	// Re-open the leaf from the held parent fd with O_NOFOLLOW: a leaf
	// that was raced into a symlink is refused (ELOOP), never followed.
	lfd, err := unix.Openat(int(parent.Fd()), leaf,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(lfd)
	if err := unix.Fchown(lfd, uid, gid); err != nil {
		return err
	}
	return nil
}
