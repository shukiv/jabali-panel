package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

// reownCall is one recorded tree of an ftpReownTree or ftpReownTrees call.
type reownCall struct {
	anchor, root string
	match        func(uint32) bool
	uid          int
	newGID       func(uint32) int
	stop         func() bool
	enter        func(*unix.Stat_t) bool
	changeIn     func(*unix.Stat_t) bool
}

// stubReownTree records the walks instead of running them and returns
// stats: one reownCall per tree.
func stubReownTree(t *testing.T, stats fsperm.ReownStats, err error) *[]reownCall {
	t.Helper()
	var calls []reownCall
	orig, origs := ftpReownTree, ftpReownTrees
	ftpReownTree = func(anchor, root string, match func(uint32) bool, uid int, newGID func(uint32) int, stop func() bool) (fsperm.ReownStats, error) {
		calls = append(calls, reownCall{anchor: anchor, root: root, match: match, uid: uid, newGID: newGID, stop: stop})
		return stats, err
	}
	ftpReownTrees = func(roots []fsperm.ReownRoot, uid int, newGID func(uint32) int, stop func() bool) (fsperm.ReownStats, error) {
		for _, r := range roots {
			calls = append(calls, reownCall{anchor: r.Anchor, root: r.Path, match: r.Match, uid: uid, newGID: newGID, stop: stop, enter: r.Enter, changeIn: r.ChangeIn})
		}
		return stats, err
	}
	t.Cleanup(func() { ftpReownTree, ftpReownTrees = orig, origs })
	return &calls
}

// stubDeletedUIDDir points the deleted-account records at a fresh directory
// the test user owns.
func stubDeletedUIDDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "deleted")
	origD, origO := ftpDeletedUIDDir, ftpDeletedUIDDirOwner
	ftpDeletedUIDDir, ftpDeletedUIDDirOwner = dir, uint32(os.Getuid())
	t.Cleanup(func() { ftpDeletedUIDDir, ftpDeletedUIDDirOwner = origD, origO })
	return dir
}

func stubLookups(t *testing.T, liveUIDs map[uint32]bool, goneGIDs map[uint32]bool) *int {
	t.Helper()
	lookups := 0
	origU, origG := ftpUIDHasPasswd, ftpGroupGone
	ftpUIDHasPasswd = func(uid uint32) bool { lookups++; return liveUIDs[uid] }
	ftpGroupGone = func(gid uint32) bool { return goneGIDs[gid] }
	t.Cleanup(func() { ftpUIDHasPasswd, ftpGroupGone = origU, origG })
	return &lookups
}

func TestParseRepquotaOrphans(t *testing.T) {
	out := `User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,BlockGrace,FileUsed,FileSoftLimit,FileHardLimit,FileGrace
root,ok,ok,2048,0,0,,40,0,0,
shop,ok,ok,1024,0,512000,,12,0,0,
shop_scan,ok,ok,10,0,51200,,1,0,0,
#1500,ok,ok,99,0,0,,9,0,0,
#1000000005,ok,ok,2048,0,0,,3,0,0,
#1000000006,ok,ok,0,0,51200,,0,0,0,
#1000000007,ok,ok,0,0,0,,1,0,0,
#1000000008,ok,ok,x,0,0,,1,0,0,
#nope,ok,ok,1,0,0,,1,0,0,
`
	got, partial := parseRepquotaOrphans(out)
	want := []ftpOrphanUID{
		{UID: 1000000005, Files: 3, UsedKB: 2048},
		{UID: 1000000007, Files: 1, UsedKB: 0}, // an empty file still counts
	}
	if len(got) != len(want) {
		t.Fatalf("orphans = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("orphan %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !partial {
		t.Error("rows that don't parse must set partial")
	}
}

func TestFtpOrphanUIDs_RefusesAnUncleanMount(t *testing.T) {
	for _, m := range []string{"", "home", "/home/../etc", "/home/"} {
		params, _ := json.Marshal(ftpOrphanUIDsParams{Mount: m})
		_, err := ftpOrphanUIDsHandler(context.Background(), params)
		wantAgentErr(t, err, agentwire.CodeInvalidArgument)
	}
}

func TestFtpOrphanUIDs_ReportsUIDsWithoutAnAccount(t *testing.T) {
	orig := runCmd
	var gotArgs []string
	runCmd = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("#1000000005,ok,ok,2048,0,0,,3,0,0,\n#1000000009,ok,ok,8,0,0,,1,0,0,\n"), nil, nil
	}
	t.Cleanup(func() { runCmd = orig })
	// 1000000009 got a passwd entry between repquota and the check.
	stubLookups(t, map[uint32]bool{1000000009: true}, nil)

	params, _ := json.Marshal(ftpOrphanUIDsParams{Mount: "/home"})
	res, err := ftpOrphanUIDsHandler(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"repquota", "-O", "csv", "/home"}; len(gotArgs) != 4 || gotArgs[3] != want[3] || gotArgs[0] != want[0] {
		t.Errorf("ran %v, want %v", gotArgs, want)
	}
	orphans := res.(map[string]any)["orphans"].([]ftpOrphanUID)
	if len(orphans) != 1 || orphans[0].UID != 1000000005 {
		t.Errorf("orphans = %+v, want only 1000000005", orphans)
	}
}

// A deleted account's maldetect scratch folder is no tenant's: its files
// don't make the uid an orphan to look for in homes.
func TestFtpOrphanUIDs_LeavesOutMaldetScratch(t *testing.T) {
	origR, origC := runCmd, ftpMaldetPubCounts
	runCmd = func(context.Context, string, ...string) ([]byte, []byte, error) {
		return []byte("#1000000005,ok,ok,16,0,0,,5,0,0,\n#1000000006,ok,ok,40,0,0,,7,0,0,\n"), nil, nil
	}
	ftpMaldetPubCounts = func(want map[uint32]bool, _ uint64) map[uint32]uint64 {
		if !want[1000000005] || !want[1000000006] {
			t.Errorf("counted for %v", want)
		}
		return map[uint32]uint64{1000000005: 5, 1000000006: 2}
	}
	t.Cleanup(func() { runCmd, ftpMaldetPubCounts = origR, origC })
	stubLookups(t, nil, nil)

	params, _ := json.Marshal(ftpOrphanUIDsParams{Mount: "/"})
	res, err := ftpOrphanUIDsHandler(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	orphans := res.(map[string]any)["orphans"].([]ftpOrphanUID)
	if len(orphans) != 1 || orphans[0].UID != 1000000006 || orphans[0].Files != 5 {
		t.Errorf("orphans = %+v, want only 1000000006 with 5 files", orphans)
	}
}

func TestMaldetPubCounts(t *testing.T) {
	pub := t.TempDir()
	orig := ftpMaldetPubDir
	ftpMaldetPubDir = pub
	t.Cleanup(func() { ftpMaldetPubDir = orig })
	for _, d := range []string{"shop_scan/quar", "shop_scan/tmp"} {
		if err := os.MkdirAll(filepath.Join(pub, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pub, "shop_scan", "event_log"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	me := uint32(os.Getuid())
	fi, err := os.Stat(pub)
	if err != nil {
		t.Fatal(err)
	}
	dev := uint64(fi.Sys().(*syscall.Stat_t).Dev)
	// pub itself + shop_scan, quar, tmp, event_log; the test user owns all.
	if got := maldetPubCounts(map[uint32]bool{me: true}, dev)[me]; got != 5 {
		t.Errorf("count = %d, want 5", got)
	}
	if got := maldetPubCounts(map[uint32]bool{me + 1: true}, dev); len(got) != 0 {
		t.Errorf("counted another uid: %v", got)
	}
	// On another filesystem than the quota mount: not in its repquota.
	if got := maldetPubCounts(map[uint32]bool{me: true}, dev+1); len(got) != 0 {
		t.Errorf("counted entries on another device: %v", got)
	}
}

func TestRemoveMaldetPubDir(t *testing.T) {
	pub := t.TempDir()
	origD, origU := ftpMaldetPubDir, maldetPubRootUID
	ftpMaldetPubDir, maldetPubRootUID = pub, uint32(os.Getuid())
	t.Cleanup(func() { ftpMaldetPubDir, maldetPubRootUID = origD, origU })
	scan := filepath.Join(pub, "shop_scan")
	if err := os.MkdirAll(filepath.Join(scan, "quar"), 0o750); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(pub, "shop_link")); err != nil {
		t.Fatal(err)
	}
	me := os.Getuid()

	if got := removeMaldetPubDir("shop_scan", me+1); got == "removed" {
		t.Error("removed a folder another uid owns")
	}
	if got := removeMaldetPubDir("shop_link", me); got == "removed" {
		t.Error("removed through a symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the link's target is gone: %v", err)
	}
	if got := removeMaldetPubDir("shop_none", me); got != "" {
		t.Errorf("absent folder: %q", got)
	}
	if got := removeMaldetPubDir("shop_scan", me); got != "removed" {
		t.Errorf("own folder: %q", got)
	}
	if _, err := os.Lstat(scan); !os.IsNotExist(err) {
		t.Errorf("folder still there: %v", err)
	}

	// pub not owned by root: nothing is touched.
	if err := os.MkdirAll(scan, 0o750); err != nil {
		t.Fatal(err)
	}
	maldetPubRootUID = uint32(me + 1)
	if got := removeMaldetPubDir("shop_scan", me); got != "" {
		t.Errorf("pub not root-owned: %q", got)
	}
}

func testFtpTenant(t *testing.T) *ftpTenant {
	t.Helper()
	u := testTenantUser(t)
	tenant, aerr := resolveFtpTenant(u.Username)
	if aerr != nil {
		t.Fatal(aerr)
	}
	return tenant
}

func TestReownDeletedAccountFiles_WalksTheJailMount(t *testing.T) {
	tenant := testFtpTenant(t)
	origM := ftpJailMounted
	var asked string
	ftpJailMounted = func(p string) (bool, error) { asked = p; return true, nil }
	t.Cleanup(func() { ftpJailMounted = origM })
	stubLookups(t, nil, map[uint32]bool{5001: true})
	calls := stubReownTree(t, fsperm.ReownStats{Reowned: map[uint32]int{1000000005: 4}}, nil)

	jail := "/var/lib/jabali-ftp-jails/" + tenant.Username + "/" + tenant.Username + "_scan"
	sum := reownDeletedAccountFiles(context.Background(), tenant, jail, 1000000005, 5000)

	if asked != filepath.Join(jail, "data") {
		t.Errorf("checked the mount of %q", asked)
	}
	if len(*calls) != 1 {
		t.Fatalf("walks = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.anchor != jail || c.root != filepath.Join(jail, "data") || c.uid != tenant.UID {
		t.Errorf("walk = %q %q to uid %d", c.anchor, c.root, c.uid)
	}
	if !c.match(1000000005) || c.match(1000000006) || c.match(uint32(tenant.UID)) {
		t.Error("match must accept the deleted uid only")
	}
	// The account's own group and a deleted group become the tenant's; any
	// other group (www-data, 33) stays.
	if c.newGID(5000) != tenant.GID || c.newGID(5001) != tenant.GID || c.newGID(33) != -1 {
		t.Error("group rule wrong")
	}
	if sum.Reowned != 4 || sum.Skipped != "" {
		t.Errorf("summary = %+v", sum)
	}
}

func TestReownDeletedAccountFiles_SkipsWithoutTheMount(t *testing.T) {
	tenant := testFtpTenant(t)
	origM := ftpJailMounted
	ftpJailMounted = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { ftpJailMounted = origM })
	calls := stubReownTree(t, fsperm.ReownStats{}, nil)

	sum := reownDeletedAccountFiles(context.Background(), tenant, "/var/lib/jabali-ftp-jails/x/y", 1000000005, 5000)
	if len(*calls) != 0 || sum.Skipped == "" {
		t.Errorf("walked without the mount: %d walks, summary %+v", len(*calls), sum)
	}
	// Never for a uid outside the reserved range (a same-uid alias).
	ftpJailMounted = func(string) (bool, error) { return true, nil }
	sum = reownDeletedAccountFiles(context.Background(), tenant, "/var/lib/jabali-ftp-jails/x/y", tenant.UID, tenant.GID)
	if len(*calls) != 0 || sum.Skipped == "" {
		t.Errorf("walked for the tenant's own uid: summary %+v", sum)
	}
}

func TestReownDeletedAccountFiles_StopsWithinTheBudget(t *testing.T) {
	tenant := testFtpTenant(t)
	origM, origB := ftpJailMounted, ftpDeleteReownBudget
	ftpJailMounted = func(string) (bool, error) { return true, nil }
	ftpDeleteReownBudget = time.Millisecond
	t.Cleanup(func() { ftpJailMounted, ftpDeleteReownBudget = origM, origB })
	calls := stubReownTree(t, fsperm.ReownStats{Stopped: true, Failed: 1, FirstError: "chown x: disk quota exceeded"}, nil)

	sum := reownDeletedAccountFiles(context.Background(), tenant, "/var/lib/jabali-ftp-jails/x/y", 1000000005, 5000)
	time.Sleep(5 * time.Millisecond)
	if !(*calls)[0].stop() {
		t.Error("stop must report true once the budget is spent")
	}
	if !sum.Stopped || sum.Failed != 1 || sum.FirstError == "" {
		t.Errorf("summary = %+v, want the stop and the failure reported", sum)
	}
}

func TestFtpReownOrphans_ReownsUIDsWithoutAnAccountOnly(t *testing.T) {
	tenant := testFtpTenant(t)
	lookups := stubLookups(t, map[uint32]bool{1000000002: true}, map[uint32]bool{6000: true})
	calls := stubReownTree(t, fsperm.ReownStats{Reowned: map[uint32]int{1000000001: 7}, Scanned: 90}, nil)

	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: tenant.Username, KeepUIDs: []uint32{1000000003}})
	res, err := ftpReownOrphansHandler(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.anchor != tenant.HomeDir || c.root != tenant.HomeDir || c.uid != tenant.UID {
		t.Errorf("walk = %q %q to uid %d", c.anchor, c.root, c.uid)
	}
	cases := map[uint32]bool{
		uint32(tenant.UID): false, // the tenant
		999999999:          false, // below the reserved range
		1000000001:         true,  // no passwd entry, no row
		1000000002:         false, // an account that exists
		1000000003:         false, // a row the reconciler will re-create
	}
	for uid, want := range cases {
		if got := c.match(uid); got != want {
			t.Errorf("match(%d) = %t, want %t", uid, got, want)
		}
	}
	c.match(1000000001)
	if *lookups != 2 {
		t.Errorf("passwd lookups = %d, want one per reserved uid", *lookups)
	}
	if c.newGID(6000) != tenant.GID || c.newGID(33) != -1 {
		t.Error("group rule wrong")
	}
	m := res.(map[string]any)
	if r := m["reowned"].(map[string]int); r[strconv.Itoa(1000000001)] != 7 {
		t.Errorf("reowned = %v", r)
	}
	if m["scanned"] != 90 {
		t.Errorf("scanned = %v", m["scanned"])
	}
}

func TestFtpReownOrphans_RefusesBadInput(t *testing.T) {
	tenant := testFtpTenant(t)
	calls := stubReownTree(t, fsperm.ReownStats{}, nil)

	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: "root"})
	if _, err := ftpReownOrphansHandler(context.Background(), params); err == nil {
		t.Error("root accepted as a tenant")
	}
	params, _ = json.Marshal(ftpReownOrphansParams{TenantUsername: tenant.Username, KeepUIDs: make([]uint32, ftpReownKeepMax+1)})
	_, err := ftpReownOrphansHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeInvalidArgument)
	if len(*calls) != 0 {
		t.Error("walked on refused input")
	}
}

func TestFtpReownOrphans_WalkErrorIsReturned(t *testing.T) {
	tenant := testFtpTenant(t)
	stubReownTree(t, fsperm.ReownStats{}, errors.New("open /home/x: too many levels of symbolic links"))
	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: tenant.Username})
	_, err := ftpReownOrphansHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeInternal)
}

func TestFtpReownOrphans_SystemUserIsNotFound(t *testing.T) {
	// root: a uid below the tenant range. resolveFtpTenant says
	// permission_denied; the sweep must see not_found.
	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: "root"})
	_, err := ftpReownOrphansHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeNotFound)
}

func TestFtpReownOrphans_NoHomeIsNotFound(t *testing.T) {
	tenant := testFtpTenant(t)
	stubReownTree(t, fsperm.ReownStats{}, fmt.Errorf("open %s: %w", tenant.HomeDir, syscall.ENOENT))
	params, _ := json.Marshal(ftpReownOrphansParams{TenantUsername: tenant.Username})
	_, err := ftpReownOrphansHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeNotFound)
}

func TestFtpReownDeadline_KeepsRoomBeforeTheCallersDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := ftpReownDeadline(ctx, time.Hour, 5*time.Second)
	if until := time.Until(d); until > 5*time.Second || until < 4*time.Second {
		t.Errorf("deadline in %s, want about 5s", until)
	}
}
