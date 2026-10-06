package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: a restore into an existing account left /home/<user> the user's
// own 0750 (rsync copies the backup's mode onto it, the chown pass makes it
// the user's), so nginx (www-data) couldn't reach the account's sites: 404.
// The home keeps the owner and mode it had.

// supplementaryGroup is one of the test user's groups other than its
// primary: a chown to it needs no root, so a changed group shows.
func supplementaryGroup(t *testing.T) int {
	t.Helper()
	gids, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gids {
		if g != os.Getgid() {
			return g
		}
	}
	t.Skip("the test user has no supplementary group")
	return -1
}

func homeModeAndGroup(t *testing.T, home string) (os.FileMode, int) {
	t.Helper()
	fi, err := os.Lstat(home)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm(), int(fi.Sys().(*syscall.Stat_t).Gid)
}

func TestAccountRestore_HomeKeepsItsOwnerAndMode(t *testing.T) {
	me := currentUsername(t)
	group := supplementaryGroup(t)
	for _, rsyncFails := range []bool{false, true} {
		root, homes := t.TempDir(), t.TempDir()
		prev := restoreHomeRoot
		restoreHomeRoot = homes
		t.Cleanup(func() { restoreHomeRoot = prev })
		home := filepath.Join(homes, me)
		if err := os.Mkdir(home, 0o751); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(home, 0o751); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(home, os.Getuid(), group); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, "home", "home", me, "index.html"), "hi")
		// rsync -a gives the home the backup's mode (here 0700); a failing
		// rsync may have done so before it failed.
		exit := "0"
		if rsyncFails {
			exit = "23"
		}
		prevExec := execCommandContext
		execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name == "rsync" {
				return exec.CommandContext(ctx, "sh", "-c", `chmod 0700 "$1"; exit `+exit, "sh", args[len(args)-1])
			}
			return exec.CommandContext(ctx, "true")
		}
		t.Cleanup(func() { execCommandContext = prevExec })
		stages := []backup.ManifestStage{{Name: backup.StageHome}}
		results := []backupRestoreStage{{Name: backup.StageHome, Status: backup.StageStatusOK}}

		_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results,
			restoreEnforcement{})

		mode, gid := homeModeAndGroup(t, home)
		if mode != 0o751 || gid != group {
			t.Errorf("rsync fails=%v: home mode %v group %d, want 0751 group %d (warnings %v)", rsyncFails, mode, gid, group, warnings)
		}
		if strings.Contains(strings.Join(warnings, "|"), "owner and mode") {
			t.Errorf("warnings %v", warnings)
		}
	}
}

func TestHomeOwnership_PutsBackWhatItSaved(t *testing.T) {
	group := supplementaryGroup(t)
	home := filepath.Join(t.TempDir(), "alice")
	if err := os.Mkdir(home, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o751|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(home, os.Getuid(), group); err != nil {
		t.Fatal(err)
	}
	saved := saveHomeOwnership(home + "/")
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := saved.put(home + "/"); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(home)
	if fi.Mode()&(os.ModePerm|os.ModeSetgid) != 0o751|os.ModeSetgid || int(fi.Sys().(*syscall.Stat_t).Gid) != group {
		t.Errorf("home %v group %d, want the saved 2751 group %d", fi.Mode(), fi.Sys().(*syscall.Stat_t).Gid, group)
	}

	// A home that wasn't there saves nothing, and putting it back is a no-op.
	missing := filepath.Join(t.TempDir(), "bob")
	if err := saveHomeOwnership(missing).put(missing); err != nil {
		t.Errorf("put of a missing home: %v", err)
	}
	// A home swapped for a link is never followed.
	link := filepath.Join(t.TempDir(), "carol")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if err := (homeOwnership{uid: os.Getuid(), gid: os.Getgid(), mode: 0o700, saved: true}).put(link); err == nil {
		t.Error("put followed a link")
	}
	if fi, _ := os.Lstat(home); fi.Mode().Perm() != 0o751 {
		t.Errorf("the link's target changed: %v", fi.Mode())
	}
}
