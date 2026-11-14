package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

func mustRecord(t *testing.T, uid int, tenant *ftpTenant) {
	t.Helper()
	if err := recordDeletedAccountUID(uid, tenant); err != nil {
		t.Fatal(err)
	}
}

func recordExists(t *testing.T, dir string, uid int) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, strconv.Itoa(uid)))
	return err == nil
}

func TestRecordDeletedAccountUID_RoundTrip(t *testing.T) {
	dir := stubDeletedUIDDir(t)
	shop := &ftpTenant{Username: "shop", UID: 5000, GID: 5000}
	mustRecord(t, 1000000005, shop)

	fi, err := os.Lstat(filepath.Join(dir, "1000000005"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Errorf("record mode %v, want a 0600 file", fi.Mode())
	}
	if di, err := os.Lstat(dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v (%v), want 0700", di.Mode(), err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "1000000005.tmp")); !os.IsNotExist(err) {
		t.Errorf("temporary file left behind: %v", err)
	}
	if got := deletedAccountUIDsOf(shop); len(got) != 1 || !got[1000000005] {
		t.Errorf("uids of shop = %v, want 1000000005", got)
	}
	// Another name with the uid, or the name with another uid: none.
	for _, other := range []*ftpTenant{{Username: "shop2", UID: 5000}, {Username: "shop", UID: 5001}} {
		if got := deletedAccountUIDsOf(other); len(got) != 0 {
			t.Errorf("uids of %s/%d = %v, want none", other.Username, other.UID, got)
		}
	}
	// Writing it again replaces it.
	mustRecord(t, 1000000005, &ftpTenant{Username: "shop2", UID: 5002})
	if got := deletedAccountUIDsOf(shop); len(got) != 0 {
		t.Errorf("uids of shop after the uid was noted for shop2 = %v", got)
	}
}

func TestRecordDeletedAccountUID_RefusesANonIsolatedUID(t *testing.T) {
	dir := stubDeletedUIDDir(t)
	shop := &ftpTenant{Username: "shop", UID: 5000}
	for _, uid := range []int{5000, ftpSubaccountUIDMin - 1, -1} {
		if err := recordDeletedAccountUID(uid, shop); err == nil {
			t.Errorf("uid %d noted", uid)
		}
	}
	if err := recordDeletedAccountUID(1000000005, &ftpTenant{Username: "x", UID: 1000000005}); err == nil {
		t.Error("the tenant's own uid noted")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("refused records still made the directory: %v", err)
	}
}

func TestDeletedAccountUIDDir_OnlyItsOwnerMayUseIt(t *testing.T) {
	shop := &ftpTenant{Username: "shop", UID: 5000}

	t.Run("open to others", func(t *testing.T) {
		dir := stubDeletedUIDDir(t)
		mustRecord(t, 1000000005, shop)
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if got := deletedAccountUIDsOf(shop); len(got) != 0 {
			t.Errorf("read records from a 0750 directory: %v", got)
		}
		if recordDeletedAccountUID(1000000006, shop) == nil {
			t.Error("wrote into a 0750 directory")
		}
		stubLookups(t, nil, nil)
		pruneDeletedAccountUIDs(nil)
		if !recordExists(t, dir, 1000000005) {
			t.Error("pruned in a 0750 directory")
		}
	})

	t.Run("another owner", func(t *testing.T) {
		stubDeletedUIDDir(t)
		mustRecord(t, 1000000005, shop)
		ftpDeletedUIDDirOwner = uint32(os.Getuid()) + 1
		if got := deletedAccountUIDsOf(shop); len(got) != 0 {
			t.Errorf("read records from a directory another uid owns: %v", got)
		}
		if recordDeletedAccountUID(1000000006, shop) == nil {
			t.Error("wrote into a directory another uid owns")
		}
	})

	t.Run("a symlink", func(t *testing.T) {
		dir := stubDeletedUIDDir(t)
		target := t.TempDir()
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		if recordDeletedAccountUID(1000000005, shop) == nil {
			t.Error("wrote through a symlink")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("files written through the symlink: %v", entries)
		}
	})
}

func TestDeletedAccountRecords_SkipJunk(t *testing.T) {
	dir := stubDeletedUIDDir(t)
	shop := &ftpTenant{Username: "shop", UID: 5000}
	mustRecord(t, 1000000005, shop)
	rec := []byte("5000 shop\n")
	put := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("abc", rec)
	put("999999999", rec)      // below the reserved range
	put("99999999999", rec)    // past uint32
	put("1000000006.tmp", rec) // a write that never finished
	// The tenant's line, but past the size cap.
	put("1000000007", []byte("5000 shop"+strings.Repeat(" ", ftpDeletedUIDRecordMax)+"\n"))
	put("1000000008", []byte("5000 shop 1\n")) // not the tenant's line
	if err := os.Mkdir(filepath.Join(dir, "1000000009"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "rec")
	if err := os.WriteFile(target, rec, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "1000000010")); err != nil {
		t.Fatal(err)
	}

	if got := deletedAccountUIDsOf(shop); len(got) != 1 || !got[1000000005] {
		t.Errorf("uids of shop = %v, want only 1000000005", got)
	}
}

func TestDeletedAccountRecords_NoDirectoryIsNone(t *testing.T) {
	dir := stubDeletedUIDDir(t)
	if got := deletedAccountUIDsOf(&ftpTenant{Username: "shop", UID: 5000}); len(got) != 0 {
		t.Errorf("uids = %v", got)
	}
	pruneDeletedAccountUIDs(nil)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("reading made the directory: %v", err)
	}
}

func TestPruneDeletedAccountUIDs_KeepsOwningAndLiveUIDs(t *testing.T) {
	dir := stubDeletedUIDDir(t)
	shop := &ftpTenant{Username: "shop", UID: 5000}
	for _, uid := range []int{1000000005, 1000000006, 1000000007} {
		mustRecord(t, uid, shop)
	}
	// 1000000006 still has its passwd entry: the delete hasn't run userdel yet.
	stubLookups(t, map[uint32]bool{1000000006: true}, nil)

	pruneDeletedAccountUIDs(map[uint32]bool{1000000005: true})

	for uid, want := range map[int]bool{1000000005: true, 1000000006: true, 1000000007: false} {
		if got := recordExists(t, dir, uid); got != want {
			t.Errorf("record %d kept = %t, want %t", uid, got, want)
		}
	}
}

func TestFtpOrphanUIDs_PrunesRecordsOfUIDsThatOwnNothing(t *testing.T) {
	dir := stubDeletedUIDDir(t)
	shop := &ftpTenant{Username: "shop", UID: 5000}
	mustRecord(t, 1000000005, shop)
	mustRecord(t, 1000000006, shop)
	stubLookups(t, nil, nil)
	origR, origC := runCmd, ftpMaldetPubCounts
	out := "#1000000005,ok,ok,8,0,0,,1,0,0,\n"
	runCmd = func(context.Context, string, ...string) ([]byte, []byte, error) { return []byte(out), nil, nil }
	ftpMaldetPubCounts = func(map[uint32]bool, uint64) map[uint32]uint64 { return nil }
	t.Cleanup(func() { runCmd, ftpMaldetPubCounts = origR, origC })
	params, _ := json.Marshal(ftpOrphanUIDsParams{Mount: "/"})

	if _, err := ftpOrphanUIDsHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if !recordExists(t, dir, 1000000005) || recordExists(t, dir, 1000000006) {
		t.Errorf("after a full listing: 1000000005 kept %t (want true), 1000000006 kept %t (want false)",
			recordExists(t, dir, 1000000005), recordExists(t, dir, 1000000006))
	}

	// A listing with rows that don't parse can't say who owns nothing.
	mustRecord(t, 1000000006, shop)
	out = "#1000000005,ok,ok,8,0,0,,1,0,0,\n#1000000008,ok,ok,x,0,0,,1,0,0,\n#nope,ok,ok,1,0,0,,1,0,0,\n"
	if _, err := ftpOrphanUIDsHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if !recordExists(t, dir, 1000000006) {
		t.Error("pruned after a partial listing")
	}
}

func TestFtpReownOrphans_NoRecordsWalksTheHomeOnly(t *testing.T) {
	tenant := testFtpTenant(t)
	stubDeletedUIDDir(t)
	stubLookups(t, nil, nil)
	calls := stubReownTree(t, fsperm.ReownStats{}, nil)
	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: tenant.Username})
	if _, err := ftpReownOrphansHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].root != tenant.HomeDir {
		t.Errorf("trees = %d, want the home only", len(*calls))
	}
}

func TestFtpReownOrphans_LooksInTheSharedTmpFoldersForTheTenantsDeletedAccounts(t *testing.T) {
	tenant := testFtpTenant(t)
	stubDeletedUIDDir(t)
	other := &ftpTenant{Username: tenant.Username + "x", UID: tenant.UID + 1}
	mustRecord(t, 1000000005, tenant) // gone
	mustRecord(t, 1000000006, tenant) // has a passwd entry again
	mustRecord(t, 1000000007, tenant) // a row the reconciler will re-create
	mustRecord(t, 1000000008, other)  // another tenant's
	stubLookups(t, map[uint32]bool{1000000006: true}, nil)
	const extraGroup = 7000
	origG := ftpTenantGroups
	ftpTenantGroups = func(*ftpTenant) map[uint32]bool {
		return map[uint32]bool{uint32(tenant.GID): true, extraGroup: true}
	}
	t.Cleanup(func() { ftpTenantGroups = origG })
	calls := stubReownTree(t, fsperm.ReownStats{}, nil)

	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: tenant.Username, KeepUIDs: []uint32{1000000007}})
	if _, err := ftpReownOrphansHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1+len(ftpSharedTmpDirs) {
		t.Fatalf("trees = %d, want the home and %v", len(*calls), ftpSharedTmpDirs)
	}
	home := (*calls)[0]
	if home.root != tenant.HomeDir || home.enter != nil || home.changeIn != nil {
		t.Errorf("home tree = %q, enter set %t, changeIn set %t", home.root, home.enter != nil, home.changeIn != nil)
	}
	if !home.match(1000000009) {
		t.Error("the home must still take any uid without an account")
	}

	dirStat := func(uid, gid int, mode uint32) *unix.Stat_t {
		return &unix.Stat_t{Uid: uint32(uid), Gid: uint32(gid), Mode: unix.S_IFDIR | mode}
	}
	for i, dir := range ftpSharedTmpDirs {
		c := (*calls)[i+1]
		if c.anchor != "/" || c.root != dir {
			t.Errorf("tree %d = %q %q, want / %q", i+1, c.anchor, c.root, dir)
		}
		for uid, want := range map[uint32]bool{
			1000000005:         true,
			1000000006:         false, // an account that exists
			1000000007:         false, // kept
			1000000008:         false, // another tenant's
			1000000009:         false, // gone but not noted for this tenant
			uint32(tenant.UID): false,
		} {
			if got := c.match(uid); got != want {
				t.Errorf("%s: match(%d) = %t, want %t", dir, uid, got, want)
			}
		}
		for _, tc := range []struct {
			name            string
			st              *unix.Stat_t
			enter, changeIn bool
		}{
			{"sticky, open to all", dirStat(0, 0, 0o1777), true, true},
			{"others may only enter", dirStat(0, 0, 0o755), true, false},
			{"closed", dirStat(0, 0, 0o700), false, false},
			{"others may only write", dirStat(0, 0, 0o702), false, false},
			{"a group the tenant is in", dirStat(0, extraGroup, 0o770), true, true},
			{"the group may only enter", dirStat(0, extraGroup, 0o750), true, false},
			{"the tenant's own", dirStat(tenant.UID, tenant.GID, 0o700), true, true},
			// The owner class is picked first, then the group.
			{"owner bits closed", dirStat(tenant.UID, 0, 0o077), false, false},
			{"group bits closed", dirStat(0, extraGroup, 0o707), false, false},
		} {
			if got := c.enter(tc.st); got != tc.enter {
				t.Errorf("%s %s: enter = %t, want %t", dir, tc.name, got, tc.enter)
			}
			if got := c.changeIn(tc.st); got != tc.changeIn {
				t.Errorf("%s %s: changeIn = %t, want %t", dir, tc.name, got, tc.changeIn)
			}
		}
	}
}

func TestFtpTenantGroups(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	gid, _ := strconv.Atoi(me.Gid)
	got := ftpTenantGroups(&ftpTenant{Username: me.Username, GID: gid})
	if !got[uint32(gid)] {
		t.Errorf("groups %v miss the primary group %d", got, gid)
	}
	if ids, err := me.GroupIds(); err == nil {
		for _, id := range ids {
			g, _ := strconv.Atoi(id)
			if !got[uint32(g)] {
				t.Errorf("groups %v miss %d", got, g)
			}
		}
	}
	if got := ftpTenantGroups(&ftpTenant{Username: "no-such-user-ftpgroups", GID: 4242}); len(got) != 1 || !got[4242] {
		t.Errorf("unknown user: %v, want only its gid", got)
	}
}
