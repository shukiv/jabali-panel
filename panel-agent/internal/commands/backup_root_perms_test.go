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
	root := filepath.Join(t.TempDir(), "backups")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var calls []fchownCall
	prevDir, prevOwner, prevChown, prevGID := backupsRootDir, backupsOwnerUID, backupsFchown, backupsGroupGID
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

func TestEnsureRestoreStaging_RefusesABackupsFolderLinkedByAnotherUser(t *testing.T) {
	root, calls := withBackupsRoot(t)
	victim := victimDir(t)
	link := filepath.Join(filepath.Dir(root), "linked-backups")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	backupsRootDir = link

	if err := ensureRestoreStaging(); err == nil {
		t.Fatal("a link that isn't root's alone must not be followed")
	}
	mustVictimUntouched(t, victim, *calls)
	mustNotExist(t, filepath.Join(victim, restoreStagingName))
}

func TestOpenDirRootLinksOnly(t *testing.T) {
	_, _ = withBackupsRoot(t)
	backupsOwnerUID = os.Getuid() // the test user plays root here
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
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
	for _, name := range []string{"abs", "rel", "real"} {
		fd, err := openDirRootLinksOnly(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: a link root owns in a folder only root can write is followed: %v", name, err)
		}
		p, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		_ = unix.Close(fd)
		if p != real {
			t.Fatalf("%s opened %s, want %s", name, p, real)
		}
	}

	// The same link in a folder others can write is not followed.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if fd, err := openDirRootLinksOnly(filepath.Join(dir, "abs")); err == nil {
		_ = unix.Close(fd)
		t.Fatal("a link in a folder others can write must not be followed")
	}
	_ = os.Chmod(dir, 0o755)

	// A link someone else owns is not followed.
	backupsOwnerUID = os.Getuid() + 1
	if fd, err := openDirRootLinksOnly(filepath.Join(dir, "abs")); err == nil {
		_ = unix.Close(fd)
		t.Fatal("a link root doesn't own must not be followed")
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
	bad := []string{
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
