package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Which tenant a deleted isolated account belonged to.
//
// A deleted account's files can also be in the server's shared temporary
// folders, where nothing says whose they are once the account is gone. So
// ftpaccount.delete notes the tenant here, and ftpaccount.reown_orphans
// looks in those folders for the tenant's noted uids. Only the agent sees
// every delete (the reconciler also removes accounts that have no panel
// row).
var (
	// ftpDeletedUIDDir holds one file per deleted isolated account, named
	// after its uid, reading "<tenant uid> <tenant name>\n". Root-only.
	ftpDeletedUIDDir = "/var/lib/jabali-ftp-deleted-uids"
	// ftpDeletedUIDDirOwner is the owner the directory must have (root;
	// tests change it).
	ftpDeletedUIDDirOwner uint32 = 0
	// ftpSharedTmpDirs are the shared temporary folders looked in. Each is
	// looked in only when it is on the home's filesystem.
	ftpSharedTmpDirs = []string{"/var/tmp", "/tmp"}
)

// ftpDeletedUIDRecordMax bounds a record file's size.
const ftpDeletedUIDRecordMax = 128

// ftpDeletedUIDDirSafe returns the record directory when it is a real
// directory owned by ftpDeletedUIDDirOwner that nobody else can read or
// write, making it first when create is set.
func ftpDeletedUIDDirSafe(create bool) (string, error) {
	dir := ftpDeletedUIDDir
	if create {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(dir, &st); err != nil {
		return "", err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Uid != ftpDeletedUIDDirOwner || st.Mode&0o077 != 0 {
		return "", fmt.Errorf("%s is not a directory only its owner can use", dir)
	}
	return dir, nil
}

// recordDeletedAccountUID notes that uid, an isolated account being
// deleted, belongs to tenant.
func recordDeletedAccountUID(uid int, tenant *ftpTenant) error {
	if uid < ftpSubaccountUIDMin || uid == tenant.UID {
		return fmt.Errorf("uid %d is not an isolated account uid", uid)
	}
	dir, err := ftpDeletedUIDDirSafe(true)
	if err != nil {
		return err
	}
	name := filepath.Join(dir, strconv.Itoa(uid))
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d %s\n", tenant.UID, tenant.Username)), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// deletedAccountRecords reads every record: uid -> its file's content.
// Anything that isn't a well-formed record is skipped.
func deletedAccountRecords() map[uint32]string {
	dir, err := ftpDeletedUIDDirSafe(false)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[uint32]string{}
	for _, e := range entries {
		uid, err := strconv.ParseUint(e.Name(), 10, 32)
		if err != nil || uid < ftpSubaccountUIDMin || !e.Type().IsRegular() {
			continue
		}
		b, err := readDeletedUIDRecord(filepath.Join(dir, e.Name()), ftpDeletedUIDRecordMax)
		if err != nil {
			continue
		}
		out[uint32(uid)] = string(bytes.TrimSpace(b))
	}
	return out
}

func readDeletedUIDRecord(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, max+1)
	n, err := f.Read(b)
	if err != nil {
		return nil, err
	}
	if int64(n) > max {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return b[:n], nil
}

// deletedAccountUIDsOf returns the uids recorded for tenant. Both the
// tenant's uid and name must match, so a renamed tenant, or a new tenant
// given a deleted one's uid, gets none of them.
func deletedAccountUIDsOf(tenant *ftpTenant) map[uint32]bool {
	want := fmt.Sprintf("%d %s", tenant.UID, tenant.Username)
	out := map[uint32]bool{}
	for uid, rec := range deletedAccountRecords() {
		if rec == want {
			out[uid] = true
		}
	}
	return out
}

// pruneDeletedAccountUIDs removes the records of uids that own nothing on
// the quota mount any more (not in owning) and have no passwd entry.
func pruneDeletedAccountUIDs(owning map[uint32]bool) {
	dir, err := ftpDeletedUIDDirSafe(false)
	if err != nil {
		return
	}
	for uid := range deletedAccountRecords() {
		if owning[uid] || ftpUIDHasPasswd(uid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, strconv.FormatUint(uint64(uid), 10)))
	}
}

// ftpTenantGroups is the tenant's primary group and the groups it is in.
var ftpTenantGroups = func(tenant *ftpTenant) map[uint32]bool {
	groups := map[uint32]bool{uint32(tenant.GID): true}
	u, err := user.Lookup(tenant.Username)
	if err != nil {
		return groups
	}
	ids, err := u.GroupIds()
	if err != nil {
		return groups
	}
	for _, id := range ids {
		if g, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32); err == nil {
			groups[uint32(g)] = true
		}
	}
	return groups
}

// ftpTenantMay reports whether the tenant's permission bits on a directory
// (owner, else group, else other, as the kernel picks) include all of need:
// 1 to go through it, 3 to also create and rename in it. ACLs aren't read,
// so a directory open to the tenant only through an ACL counts as closed.
func ftpTenantMay(tenant *ftpTenant, groups map[uint32]bool, st *unix.Stat_t, need uint32) bool {
	var bits uint32
	switch {
	case st.Uid == uint32(tenant.UID):
		bits = (st.Mode >> 6) & 7
	case groups[st.Gid]:
		bits = (st.Mode >> 3) & 7
	default:
		bits = st.Mode & 7
	}
	return bits&need == need
}
