package commands

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// moodle_data_heal_test.go — Moodle installs made before the data root became
// owner-only keep a .moodledata directory in group www-data with a data root
// of 0777 inside it. At boot the agent puts .moodledata in the account's own
// group at 0750, which takes the web server's group off every data root under
// it without touching Moodle's files or config.php.

type moodleHealFixture struct {
	root string
	ran  []string
}

func newMoodleHealFixture(t *testing.T) *moodleHealFixture {
	t.Helper()
	f := &moodleHealFixture{root: t.TempDir()}
	prevRoot, prevLookup, prevExec := moodleHomeRoot, lookupAccountIDs, execCommandContext
	moodleHomeRoot = f.root
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.ran = append(f.ran, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { moodleHomeRoot, lookupAccountIDs, execCommandContext = prevRoot, prevLookup, prevExec })
	return f
}

// dataParent makes <root>/<user>/domains/<domain>/.moodledata with mode.
func (f *moodleHealFixture) dataParent(t *testing.T, user, domain string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(f.root, user, "domains", domain, ".moodledata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dirGID(t *testing.T, dir string) uint32 {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Gid
}

func moodleHealLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLockMoodleDataParents_WebServerGroupTakenOff(t *testing.T) {
	for name, tc := range map[string]struct {
		mode     os.FileMode
		ownGroup bool
	}{
		// What every install before this has: the domain directory's group.
		"group www-data, 0755": {0o755, false},
		"own group, 0755":      {0o755, true},
		"group www-data, 0750": {0o750, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMoodleHealFixture(t)
			dir := f.dataParent(t, "alice", "a.test", tc.mode)
			// The directory's current group stands in for www-data, or for
			// alice's own group when it already is that.
			own := dirGID(t, dir)
			if !tc.ownGroup {
				own++
			}
			lookupAccountIDs = func(name string) (uint32, uint32, error) {
				if name == "alice" {
					return uint32(os.Getuid()), own, nil
				}
				return 0, 0, errors.New("unknown user")
			}
			LockMoodleDataParents(context.Background(), moodleHealLog())
			want := []string{"chgrp --no-dereference alice " + dir, "chmod 0750 " + dir}
			if len(f.ran) != len(want) {
				t.Fatalf("ran %q, want %q", f.ran, want)
			}
			for i, w := range want {
				// As the account, so nothing it planted can redirect a root-side change.
				if !strings.Contains(f.ran[i], "--uid=alice") || !strings.HasSuffix(f.ran[i], w) {
					t.Errorf("step %d ran %q, want %q as alice", i, f.ran[i], w)
				}
			}
		})
	}
}

func TestLockMoodleDataParents_LeavesTheRestAlone(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, f *moodleHealFixture){
		// Already the account's group at 0750 (every install from now on).
		"already locked": func(t *testing.T, f *moodleHealFixture) {
			dir := f.dataParent(t, "alice", "a.test", 0o750)
			gid := dirGID(t, dir)
			lookupAccountIDs = func(string) (uint32, uint32, error) { return uint32(os.Getuid()), gid, nil }
		},
		"a link in its place": func(t *testing.T, f *moodleHealFixture) {
			parent := filepath.Join(f.root, "alice", "domains", "a.test")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(parent, ".moodledata")); err != nil {
				t.Fatal(err)
			}
			lookupAccountIDs = func(string) (uint32, uint32, error) { return uint32(os.Getuid()), 1, nil }
		},
		"owned by someone else": func(t *testing.T, f *moodleHealFixture) {
			f.dataParent(t, "alice", "a.test", 0o755)
			lookupAccountIDs = func(string) (uint32, uint32, error) { return uint32(os.Getuid()) + 1, 1, nil }
		},
		"no such account": func(t *testing.T, f *moodleHealFixture) {
			f.dataParent(t, "alice", "a.test", 0o755)
			lookupAccountIDs = func(string) (uint32, uint32, error) { return 0, 0, errors.New("unknown user") }
		},
		"not a valid account name": func(t *testing.T, f *moodleHealFixture) {
			f.dataParent(t, "Bad Name", "a.test", 0o755)
			lookupAccountIDs = func(string) (uint32, uint32, error) { return uint32(os.Getuid()), 1, nil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMoodleHealFixture(t)
			setup(t, f)
			LockMoodleDataParents(context.Background(), moodleHealLog())
			if len(f.ran) != 0 {
				t.Fatalf("ran %q", f.ran)
			}
		})
	}
}

// The heal only reaches existing servers if the agent runs it at start.
func TestAgentMainLocksMoodleDataParentsAtBoot(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "jabali-agent", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "commands.LockMoodleDataParents(") {
		t.Fatal("jabali-agent main.go never calls commands.LockMoodleDataParents")
	}
}
