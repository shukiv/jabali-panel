package commands

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// tenantHomeRoot is the directory that holds tenant homes. A variable only so
// tests can point it at a temporary tree.
var tenantHomeRoot = "/home"

// tenantHomeOf returns the home directory and account name that own path. The
// path must sit under tenantHomeRoot/<account>/ with a valid account name.
func tenantHomeOf(path string) (home, account string, err error) {
	rel, err := filepath.Rel(tenantHomeRoot, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("path %q is not under a tenant home", path)
	}
	account = strings.SplitN(rel, string(os.PathSeparator), 2)[0]
	if !usernameRegex.MatchString(account) {
		return "", "", fmt.Errorf("path %q: invalid account %q", path, account)
	}
	return filepath.Join(tenantHomeRoot, account), account, nil
}

// tenantScope is a filesafe scope confined to the home that owns path. Opens
// through it resolve beneath that home, so no symlink in the tenant's tree can
// send a root-side read or write to a file outside it.
func tenantScope(path string) (*filesafe.Scope, error) {
	home, account, err := tenantHomeOf(path)
	if err != nil {
		return nil, err
	}
	return filesafe.NewScope(account, account, []string{home})
}

// tenantReadMax caps a root-side read of a tenant-controlled file. Version and
// config probes never need more.
const tenantReadMax = 1 << 20

// readTenantRegular returns the contents of a regular file under a tenant home. It
// refuses a symlink or any path that resolves outside the home, does not block
// on a FIFO, and refuses a file over tenantReadMax bytes.
func readTenantRegular(path string) ([]byte, error) {
	scope, err := tenantScope(path)
	if err != nil {
		return nil, err
	}
	f, err := scope.OpenInScope(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, tenantReadMax+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > tenantReadMax {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, tenantReadMax)
	}
	return b, nil
}

// writeTenantFile creates or replaces a regular file under a tenant home. The
// file is opened through the scope with O_NOFOLLOW, so a symlink planted at the
// leaf is refused rather than written through. Owner and mode are set on the
// open descriptor, not by path.
func writeTenantFile(path string, data []byte, perm os.FileMode, uid, gid int) error {
	scope, err := tenantScope(path)
	if err != nil {
		return err
	}
	f, err := scope.OpenInScope(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// lookupOwnerIDs returns the numeric uid of account and gid of group.
func lookupOwnerIDs(account, group string) (uid, gid int, err error) {
	u, err := user.Lookup(account)
	if err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, err
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err = strconv.Atoi(g.Gid)
	return uid, gid, err
}
