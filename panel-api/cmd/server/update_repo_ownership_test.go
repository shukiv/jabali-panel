package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// initRepoWithTrackedFiles builds a throwaway git repo with the given tracked
// files and returns its path.
func initRepoWithTrackedFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

// The scope decision matters as much as the repair: install.sh's chown is
// deliberately narrow "so node_modules or other trees that may legitimately be
// group-owned differently don't get clobbered". A blanket chown -R over the
// repo would take ownership of node_modules and build caches. Only TRACKED
// files may be touched.
func TestOwnershipRepairOnlyConsidersTrackedFiles(t *testing.T) {
	repo := initRepoWithTrackedFiles(t, map[string]string{
		"CLAUDE.md":            "tracked",
		"panel-ui/src/main.ts": "tracked",
	})
	// Untracked, and deliberately not in the index — node_modules stands in for
	// anything with its own ownership.
	untracked := filepath.Join(repo, "panel-ui", "node_modules", "pkg", "index.js")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(untracked, []byte("x"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	me, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	rep, err := fixTrackedFileOwnership(context.Background(), repo, me.Username)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if rep.Scanned != 2 {
		t.Errorf("scanned %d files, want the 2 TRACKED ones — an untracked file "+
			"under node_modules must never be considered", rep.Scanned)
	}
	// Everything already belongs to the caller, so nothing needed changing.
	if len(rep.Fixed) != 0 {
		t.Errorf("nothing was mis-owned, but the pass reported fixing %v", rep.Fixed)
	}
}

// A tracked file the service user cannot read is the whole bug: repo files are
// mode 0640/0660, so one root-owned tracked file breaks every git command that
// hashes the working tree.
//
//	error: open("CLAUDE.md"): Permission denied
//	fatal: cannot hash CLAUDE.md
//
// Reproduced on a fresh install. Changing ownership needs root, so the repair
// itself can only be exercised as root; the detection half is checked either
// way.
func TestOwnershipRepairDetectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a file owned by another user")
	}
	repo := initRepoWithTrackedFiles(t, map[string]string{"CLAUDE.md": "tracked"})
	target := filepath.Join(repo, "CLAUDE.md")
	if err := os.Chown(target, 0, 0); err != nil {
		t.Fatalf("chown root: %v", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody user")
	}
	rep, err := fixTrackedFileOwnership(context.Background(), repo, nobody.Username)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(rep.Fixed) != 1 || rep.Fixed[0] != "CLAUDE.md" {
		t.Fatalf("want CLAUDE.md repaired, got %v", rep.Fixed)
	}
}

func TestOwnershipRepairRejectsUnknownUser(t *testing.T) {
	repo := initRepoWithTrackedFiles(t, map[string]string{"a.txt": "x"})
	if _, err := fixTrackedFileOwnership(context.Background(), repo, "definitely-not-a-user-12345"); err == nil {
		t.Error("an unknown service user must be an error, not a silent no-op")
	}
}

// A file that is tracked but missing from the working tree is normal mid-reset;
// it must not abort the pass.
func TestOwnershipRepairToleratesMissingWorktreeFile(t *testing.T) {
	repo := initRepoWithTrackedFiles(t, map[string]string{"a.txt": "x", "b.txt": "y"})
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	me, _ := user.Current()
	rep, err := fixTrackedFileOwnership(context.Background(), repo, me.Username)
	if err != nil {
		t.Fatalf("a deleted tracked file must not fail the pass: %v", err)
	}
	if rep.Scanned != 2 {
		t.Errorf("scanned %d, want 2 (both still tracked)", rep.Scanned)
	}
}

// The operator sees this line instead of git's raw stderr, so it has to carry
// the actual cause.
func TestGitFailureLinePrefersTheFatalLine(t *testing.T) {
	out := "error: open(\"CLAUDE.md\"): Permission denied\nfatal: cannot hash CLAUDE.md\n"
	if got := gitFailureLine(out, errors.New("exit status 128")); got != "fatal: cannot hash CLAUDE.md" {
		t.Errorf("got %q, want the fatal: line — that is the one naming the cause", got)
	}
	if got := gitFailureLine("", errors.New("exit status 128")); !strings.Contains(got, "exit status 128") {
		t.Errorf("with no output, fall back to the error: got %q", got)
	}
	if got := gitFailureLine("just a warning\n", nil); got != "just a warning" {
		t.Errorf("got %q, want the first non-empty line", got)
	}
}
