package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// channelRepo is a real git checkout with commits A (migrations 1-2) and B
// (adds 3), tagged a and b, plus a commit s on a branch that forks from A and
// adds no migration. HEAD is left on s; tests check out what they need.
func channelRepo(t *testing.T) (string, gitOutFunc) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitOut := func(args ...string) (string, error) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := gitOut(args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	migrations := filepath.Join(dir, "panel-api", "internal", "db", "migrations")
	if err := os.MkdirAll(migrations, 0o755); err != nil {
		t.Fatal(err)
	}
	add := func(names ...string) {
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(migrations, n), []byte("SELECT 1;\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	must("init", "-q", "-b", "main")
	add("000001_a.up.sql", "000001_a.down.sql", "000002_b.up.sql", "000002_b.down.sql")
	must("add", ".")
	must("commit", "-q", "-m", "A")
	must("tag", "a")
	must("branch", "side")
	add("000003_c.up.sql", "000003_c.down.sql")
	must("add", ".")
	must("commit", "-q", "-m", "B")
	must("tag", "b")
	must("checkout", "-q", "side")
	if err := os.WriteFile(filepath.Join(dir, "side.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("add", "side.txt")
	must("commit", "-q", "-m", "side")
	must("tag", "s")
	return dir, gitOut
}

func checkout(t *testing.T, gitOut gitOutFunc, ref string) {
	t.Helper()
	if out, err := gitOut("checkout", "-q", ref); err != nil {
		t.Fatalf("checkout %s: %v: %s", ref, err, out)
	}
}

// The fleet case: a box that followed main is switched to the stable channel
// while `stable` is behind it. It stays on its current build instead of
// resetting to the older tree.
func TestUpdateResetTarget_StableBehindHeadStaysOnCurrentBuild(t *testing.T) {
	_, gitOut := channelRepo(t)
	checkout(t, gitOut, "b")

	ref, note, err := updateResetTarget(gitOut, "a", true, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "HEAD" {
		t.Fatalf("ref = %q, want HEAD (stay on the current build)", ref)
	}
	if !strings.Contains(note, "staying on the current build") {
		t.Errorf("the operator should be told why, got %q", note)
	}
}

// A stable release ahead of the box, or the same build, is followed.
func TestUpdateResetTarget_StableAheadOrEqualIsFollowed(t *testing.T) {
	_, gitOut := channelRepo(t)
	checkout(t, gitOut, "a")
	for _, target := range []string{"b", "a"} {
		ref, note, err := updateResetTarget(gitOut, target, true, 2)
		if err != nil || ref != target || note != "" {
			t.Errorf("target %s: got ref=%q note=%q err=%v, want it followed", target, ref, note, err)
		}
	}
}

// A target whose migrations stop short of the live schema is refused before
// the reset, on any channel, even when it is not simply behind HEAD (a
// diverged stable, or main on the development channel).
func TestUpdateResetTarget_TargetBehindLiveSchemaIsRefused(t *testing.T) {
	_, gitOut := channelRepo(t)
	checkout(t, gitOut, "b")
	for _, tc := range []struct {
		target       string
		followStable bool
	}{{"s", true}, {"s", false}, {"a", false}} {
		_, _, err := updateResetTarget(gitOut, tc.target, tc.followStable, 3)
		if err == nil || !strings.Contains(err.Error(), "only knows migrations up to 2") ||
			!strings.Contains(err.Error(), "schema version 3") {
			t.Errorf("target %s (stable=%v): want a refusal naming both versions, got %v", tc.target, tc.followStable, err)
		}
	}
	// Without a database to ask (a dev checkout) there is nothing to compare.
	if ref, _, err := updateResetTarget(gitOut, "s", false, 0); err != nil || ref != "s" {
		t.Errorf("no live schema: got ref=%q err=%v, want s", ref, err)
	}
}

// Staying put needs no git at all, and a git failure stops the update rather
// than resetting blind.
func TestUpdateResetTarget_HeadIsANoOpAndGitFailuresStop(t *testing.T) {
	called := false
	ref, _, err := updateResetTarget(func(...string) (string, error) {
		called = true
		return "", nil
	}, "HEAD", true, 3)
	if err != nil || ref != "HEAD" || called {
		t.Errorf("HEAD: got ref=%q err=%v git called=%v", ref, err, called)
	}

	_, _, err = updateResetTarget(func(...string) (string, error) {
		return "fatal: not a git repository", errors.New("exit status 128")
	}, "refs/tags/stable", true, 3)
	if err == nil {
		t.Error("a git failure must stop the update")
	}
}

func TestRefMaxMigration(t *testing.T) {
	_, gitOut := channelRepo(t)
	for ref, want := range map[string]uint{"a": 2, "b": 3, "s": 2} {
		got, err := refMaxMigration(gitOut, ref)
		if err != nil || got != want {
			t.Errorf("%s: got %d, %v; want %d", ref, got, err, want)
		}
	}
	if _, err := refMaxMigration(func(...string) (string, error) { return "README.md\n", nil }, "x"); err == nil {
		t.Error("a tree with no migrations must be an error, not version 0")
	}
}

// The guard only helps if the update asks it before the reset: every step
// from the reset to the binary swap runs from the reset tree.
func TestUpdate_ChecksResetTargetBeforeReset(t *testing.T) {
	src := stripLineComments(readGoSource(t, "update.go"))
	guard := strings.Index(src, "updateResetTarget(")
	reset := strings.Index(src, `asUser(repoDir, "git", "reset", "--hard", resetRef)`)
	if guard < 0 || reset < 0 {
		t.Fatalf("not found: updateResetTarget at %d, reset at %d", guard, reset)
	}
	if guard > reset {
		t.Fatal("updateResetTarget must run before git reset --hard")
	}
	if !strings.Contains(src[guard:reset], "resetRef = target") {
		t.Error("the update must reset to the target the guard returns")
	}
}
