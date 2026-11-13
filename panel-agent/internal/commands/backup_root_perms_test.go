package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type fchownCall struct {
	path     string
	uid, gid int
}

// withBackupsRoot points the backups folder at a temp dir. Files the test makes
// are the test user's, so with backupsOwnerUID left at 0 they are not "root's";
// the chown is recorded, not made.
func withBackupsRoot(t *testing.T) (string, *[]fchownCall) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "backups")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var calls []fchownCall
	prevDir, prevOwner, prevChown, prevGID := backupsRootDir, backupsOwnerUID, backupsFchown, backupsGroupGID
	prevTrusted, prevBase := backupsTrustedUID, backupsWalkBase
	t.Cleanup(func() { backupsTrustedUID, backupsWalkBase = prevTrusted, prevBase })
	// The walk starts at the test's temp dir, and the test user, who owns it,
	// is trusted above the folder.
	backupsWalkBase = base
	backupsTrustedUID = os.Getuid()
	backupsRootDir = root
	backupsOwnerUID = 0
	backupsFchown = func(fd, uid, gid int) error {
		p, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		calls = append(calls, fchownCall{p, uid, gid})
		return nil
	}
	backupsGroupGID = func() (int, error) { return 4242, nil }
	t.Cleanup(func() {
		backupsRootDir, backupsOwnerUID, backupsFchown, backupsGroupGID = prevDir, prevOwner, prevChown, prevGID
	})
	return root, &calls
}

func mustMode(t *testing.T, p string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s: mode %o, want %o", p, got, want)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should be gone, Lstat err = %v", p, err)
	}
}

// victimDir is a folder outside the backups folder that a link points at; the
// heal must never touch it.
func victimDir(t *testing.T) string {
	t.Helper()
	v := filepath.Join(t.TempDir(), "victim")
	if err := os.Mkdir(v, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustVictimUntouched(t *testing.T, v string, calls []fchownCall) {
	t.Helper()
	mustMode(t, v, 0o755)
	if _, err := os.Stat(filepath.Join(v, "keep")); err != nil {
		t.Fatalf("victim's file is gone: %v", err)
	}
	for _, c := range calls {
		if c.path == v {
			t.Fatalf("chown reached the link's target %s", v)
		}
	}
}

func TestEnsureRestoreStaging_GivesTheFolderBackToRoot(t *testing.T) {
	root, calls := withBackupsRoot(t)
	victim := victimDir(t)

	if err := os.Symlink(victim, filepath.Join(root, "repo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "downloads"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "downloads"), 0o777); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".restore.lock", ".init-0123abcd.lock", "keep.sql"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	staging := filepath.Join(root, restoreStagingName)
	if err := os.MkdirAll(filepath.Join(staging, "01JABCDEFGHJKMNPQRSTVWXYZ0-upload", "home"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(staging, "01JABCDEFGHJKMNPQRSTVWXYZ1")); err != nil {
		t.Fatal(err)
	}

	if err := ensureRestoreStaging(); err != nil {
		t.Fatalf("ensureRestoreStaging: %v", err)
	}

	mustMode(t, root, 0o750)
	mustNotExist(t, filepath.Join(root, "repo"))
	mustMode(t, filepath.Join(root, "downloads"), 0o755)
	mustNotExist(t, filepath.Join(root, ".restore.lock"))
	mustNotExist(t, filepath.Join(root, ".init-0123abcd.lock"))
	if _, err := os.Stat(filepath.Join(root, "keep.sql")); err != nil {
		t.Fatalf("a file root didn't make must be left alone: %v", err)
	}
	mustMode(t, staging, 0o700)
	if left, _ := os.ReadDir(staging); len(left) != 0 {
		t.Fatalf("restore-staging wasn't root's, so nothing in it can be a stage the agent made; left %v", left)
	}
	mustVictimUntouched(t, victim, *calls)

	got := map[string]fchownCall{}
	for _, c := range *calls {
		got[filepath.Base(c.path)] = c
	}
	if c := got["backups"]; c.uid != 0 || c.gid != 4242 {
		t.Fatalf("backups folder chown = %+v, want root and the panel group", c)
	}
	for _, name := range []string{"downloads", restoreStagingName} {
		if c, ok := got[name]; !ok || c.uid != 0 || c.gid != -1 {
			t.Fatalf("%s chown = %+v (made: %v), want root, group kept", name, c, ok)
		}
	}
}

func TestEnsureRestoreStaging_ReplacesALinkWhereStagingBelongs(t *testing.T) {
	root, calls := withBackupsRoot(t)
	victim := victimDir(t)
	if err := os.Symlink(victim, filepath.Join(root, restoreStagingName)); err != nil {
		t.Fatal(err)
	}

	if err := ensureRestoreStaging(); err != nil {
		t.Fatalf("ensureRestoreStaging: %v", err)
	}

	fi, err := os.Lstat(filepath.Join(root, restoreStagingName))
	if err != nil || !fi.IsDir() {
		t.Fatalf("restore-staging should be a real folder now: %v %v", fi, err)
	}
	mustMode(t, filepath.Join(root, restoreStagingName), 0o700)
	mustVictimUntouched(t, victim, *calls)
}

func TestEnsureRestoreStaging_RefusesAFolderOthersCanReplace(t *testing.T) {
	_, calls := withBackupsRoot(t)
	victim := victimDir(t)
	open := filepath.Join(backupsWalkBase, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(open, 0o755) })

	// A link, then a real folder, in a folder anyone can write: either could
	// be swapped after the check, so neither is used.
	link := filepath.Join(open, "backups-link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(open, "backups")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, real} {
		backupsRootDir = p
		if err := ensureRestoreStaging(); err == nil {
			t.Fatalf("%s: a folder others can replace must not be used", p)
		}
	}
	mustVictimUntouched(t, victim, *calls)
	mustNotExist(t, filepath.Join(victim, restoreStagingName))
	mustNotExist(t, filepath.Join(real, restoreStagingName))
	if len(*calls) != 0 {
		t.Fatalf("nothing may be chowned: %+v", *calls)
	}
}

func TestOpenDirRootLinksOnly(t *testing.T) {
	_, _ = withBackupsRoot(t)
	dir := filepath.Join(backupsWalkBase, "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "rel")); err != nil {
		t.Fatal(err)
	}
	opens := func(name string) bool {
		fd, err := openDirRootLinksOnly(filepath.Join(dir, name))
		if err != nil {
			return false
		}
		p, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		_ = unix.Close(fd)
		if p != real {
			t.Fatalf("%s opened %s, want %s", name, p, real)
		}
		return true
	}
	for _, name := range []string{"abs", "rel", "real"} {
		if !opens(name) {
			t.Fatalf("%s: in a folder only a trusted owner can write, it opens", name)
		}
	}

	// Sticky like /tmp: what a trusted owner put there still opens.
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if !opens("abs") {
		t.Fatal("a trusted link in a sticky folder opens")
	}

	// Writable by others and not sticky: nothing in it opens.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"abs", "real"} {
		if opens(name) {
			t.Fatalf("%s: in a folder others can write, it must not open", name)
		}
	}
	_ = os.Chmod(dir, 0o755)

	// Folders owned by an untrusted user: nothing below them opens.
	backupsTrustedUID = os.Getuid() + 1
	if opens("real") {
		t.Fatal("below a folder an untrusted user owns, nothing opens")
	}
}

func TestTrustedParentAndOwner(t *testing.T) {
	prev := backupsTrustedUID
	backupsTrustedUID = 0
	t.Cleanup(func() { backupsTrustedUID = prev })
	st := func(uid uint32, mode uint32) *unix.Stat_t { return &unix.Stat_t{Uid: uid, Mode: mode} }
	cases := []struct {
		name          string
		parent, child *unix.Stat_t
		want          bool
	}{
		{"root's 0755", st(0, unix.S_IFDIR|0o755), st(1000, unix.S_IFDIR|0o755), true},
		{"panel user's 0755", st(1000, unix.S_IFDIR|0o755), st(0, unix.S_IFDIR|0o755), false},
		{"root's, group-writable", st(0, unix.S_IFDIR|0o775), st(0, unix.S_IFDIR|0o755), false},
		{"root's, other-writable", st(0, unix.S_IFDIR|0o777), st(0, unix.S_IFDIR|0o755), false},
		{"sticky, root's child", st(0, unix.S_IFDIR|unix.S_ISVTX|0o777), st(0, unix.S_IFLNK|0o777), true},
		{"sticky, panel user's child", st(0, unix.S_IFDIR|unix.S_ISVTX|0o777), st(1000, unix.S_IFLNK|0o777), false},
	}
	for _, c := range cases {
		if got := trustedParent(c.parent, c.child); got != c.want {
			t.Errorf("trustedParent %s = %v, want %v", c.name, got, c.want)
		}
	}
	if !trustedOwner(0) || trustedOwner(1000) {
		t.Error("trustedOwner: only root, with no test user set")
	}
}

func TestEnsureRestoreStaging_KeepsStagesWhenStagingIsRoots(t *testing.T) {
	root, calls := withBackupsRoot(t)
	backupsOwnerUID = os.Getuid() // everything here is "root's"
	stage := filepath.Join(root, restoreStagingName, "01JABCDEFGHJKMNPQRSTVWXYZ0")
	if err := os.MkdirAll(stage, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, restoreStagingName), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := ensureRestoreStaging(); err != nil {
		t.Fatalf("ensureRestoreStaging: %v", err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("a stage in root's restore-staging must stay: %v", err)
	}
	mustMode(t, filepath.Join(root, restoreStagingName), 0o700)
	for _, c := range *calls {
		if filepath.Base(c.path) == restoreStagingName {
			t.Fatalf("restore-staging was root's already; chown %+v", c)
		}
	}
}

func TestPeerIsRoot_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"bare context", context.Background(), false},
		{"uid not resolved", WithPeerIdentity(context.Background(), 0, false, true), false},
		{"panel user, admin capable", WithPeerIdentity(context.Background(), 1000, true, true), false},
		{"root", WithPeerIdentity(context.Background(), 0, true, true), true},
	}
	for _, c := range cases {
		if got := PeerIsRoot(c.ctx); got != c.want {
			t.Errorf("%s: PeerIsRoot = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestApplySystemFromTar_RunsOnlyForRoot(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"container_path": "/var/lib/jabali-uploads/sysrestore-x.tar", "apply": true})
	for name, ctx := range map[string]context.Context{
		"no identity":               context.Background(),
		"panel user, admin capable": WithPeerIdentity(context.Background(), 1000, true, true),
		"uid not resolved, uid 0":   WithPeerIdentity(context.Background(), 0, false, true),
	} {
		_, err := systemFullbackupApplySystemHandler(ctx, raw)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodePermissionDenied {
			t.Errorf("%s: err = %v, want permission denied", name, err)
		}
	}
}

func TestSysRestoreContainerPathClean(t *testing.T) {
	root, _ := withBackupsRoot(t)
	backupsOwnerUID = os.Getuid()
	staging := filepath.Join(root, restoreStagingName)
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(staging, "sysrestore-01JABCDEFGHJKMNPQRSTVWXYZ0.tar")
	if err := os.WriteFile(good, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(staging, "sysrestore-01JABCDEFGHJKMNPQRSTVWXYZ1.tar")
	if err := os.Symlink(good, linked); err != nil {
		t.Fatal(err)
	}
	if got, err := sysRestoreContainerPathClean(good); err != nil || got != good {
		t.Fatalf("root's copy in restore-staging: got %q, %v", got, err)
	}
	elsewhere := filepath.Join(t.TempDir(), "sysrestore-01JABCDEFGHJKMNPQRSTVWXYZ0.tar")
	if err := os.WriteFile(elsewhere, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		elsewhere,
		"/var/lib/jabali-uploads/sysrestore-01JABCDEFGHJKMNPQRSTVWXYZ0.tar",
		filepath.Join(staging, "other.tar"),
		staging + "/sub/../sysrestore-01JABCDEFGHJKMNPQRSTVWXYZ0.tar",
		linked,
	}
	for _, p := range bad {
		if _, err := sysRestoreContainerPathClean(p); err == nil {
			t.Errorf("%s: accepted", p)
		}
	}
	backupsOwnerUID = os.Getuid() + 1
	if _, err := sysRestoreContainerPathClean(good); err == nil {
		t.Error("a copy root doesn't own: accepted")
	}
}

func TestEvictStaleSysApplyStages_SweepsRestoreStaging(t *testing.T) {
	root, _ := withBackupsRoot(t)
	staging := filepath.Join(root, restoreStagingName)
	old := filepath.Join(staging, sysApplyStagePrefix+"01JABCDEFGHJKMNPQRSTVWXYZ0")
	oldCopy := filepath.Join(staging, sysRestoreCopyPrefix+"01JABCDEFGHJKMNPQRSTVWXYZ0.tar")
	fresh := filepath.Join(staging, sysApplyStagePrefix+"01JABCDEFGHJKMNPQRSTVWXYZ1")
	for _, d := range []string{old, fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(oldCopy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * sysApplyStageTTL)
	for _, p := range []string{old, oldCopy} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	evictStaleSysApplyStages()
	left, _ := os.ReadDir(staging)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != filepath.Base(fresh) {
		t.Fatalf("left %v, want only the fresh stage", names)
	}
}

// As root, the two cases a test user can't build: a link the panel's user left
// in a folder that is root's now (it planted the link before an update gave the
// folder to root) is not followed, and an entry in root's restore-staging that
// isn't root's is removed while root's stage stays.
func TestBackupsRoot_AsRoot_ForeignLinkAndForeignStage(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root, to give test files to another user")
	}
	const nobody = 65534
	root, calls := withBackupsRoot(t)
	victim := victimDir(t)

	dir := filepath.Join(backupsWalkBase, "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "backups")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(link, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	backupsRootDir = link
	if err := ensureRestoreStaging(); err == nil {
		t.Fatal("a link another user owns must not be followed, even in root's folder")
	}
	mustVictimUntouched(t, victim, *calls)
	mustNotExist(t, filepath.Join(victim, restoreStagingName))

	backupsRootDir = root
	staging := filepath.Join(root, restoreStagingName)
	keep := filepath.Join(staging, "01JABCDEFGHJKMNPQRSTVWXYZ0")
	foreign := filepath.Join(staging, "01JABCDEFGHJKMNPQRSTVWXYZ1-upload")
	for _, d := range []string{keep, foreign} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(foreign, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	if err := ensureRestoreStaging(); err != nil {
		t.Fatalf("ensureRestoreStaging: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("root's stage must stay: %v", err)
	}
	mustNotExist(t, foreign)
}
