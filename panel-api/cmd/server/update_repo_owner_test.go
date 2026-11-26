package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every re-clone has to go through recloneAt, which puts the fresh clone back
// on the running build. A clone left on main's tip was read by `jabali update`
// as the build the server runs, so a stable server kept and built main.
func TestRecloneOnlyThroughRecloneAt(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "update_repo_owner.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "recloneArgs(") {
			t.Errorf("%s clones the checkout with recloneArgs directly; use recloneAtBuild", f)
		}
	}
}

func TestRecloneAt_RefusesBuildWithoutCommit(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "jabali-panel")
	for _, c := range []string{"unknown", "", "a1f345aeb", "main"} {
		if err := recloneAt(dst, c); err == nil {
			t.Errorf("recloneAt(%q) = nil, want a refusal", c)
		}
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a refused re-clone created %s", dst)
	}
}

func TestResetCloneToBuild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jabali-panel")
	git := func(args ...string) string {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "build")
	build := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "main tip")

	if err := resetCloneToBuild(dir, build); err != nil {
		t.Fatal(err)
	}
	if head := git("rev-parse", "HEAD"); head != build {
		t.Fatalf("HEAD = %s, want the build %s", head, build)
	}

	if err := resetCloneToBuild(dir, strings.Repeat("0", 40)); err == nil {
		t.Fatal("reset to a commit the clone lacks succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a clone that could not be put on the build was left in place")
	}
}

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

// Repo config and hooks must not reach root's git: the -c overrides win over
// anything in .git/config.
func TestGitRootCmdOverridesRepoConfig(t *testing.T) {
	c := gitRootCmd("/opt/x", "status")
	joined := strings.Join(c.Args, " ")
	for _, want := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "core.pager=cat", "-C /opt/x", "status"} {
		if !strings.Contains(joined, want) {
			t.Errorf("git args %q missing %q", joined, want)
		}
	}
	env := strings.Join(c.Env, "\n")
	if !strings.Contains(env, "GIT_CONFIG_GLOBAL=/dev/null") || !strings.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("git env does not drop system/global config")
	}
	if got := gitRootCmd("", "clone").Args; strings.Contains(strings.Join(got, " "), "-C") {
		t.Errorf("empty dir must not pass -C: %v", got)
	}
}

// A checkout whose .git is not root-owned (every non-root test user) is the
// old jabali-owned layout: it is moved aside and re-cloned, never reused.
func TestEnforceRepoOwnershipMovesJabaliOwnedCheckoutAside(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "jabali-panel")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "panel-ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	reclonedTo := ""
	migrated, err := enforceRepoOwnership(repo,
		func(name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		func(dst string) error {
			reclonedTo = dst
			if err := os.MkdirAll(filepath.Join(dst, ".git"), 0o755); err != nil {
				return err
			}
			return os.MkdirAll(filepath.Join(dst, "panel-ui"), 0o755)
		})
	if err != nil {
		t.Fatalf("enforceRepoOwnership: %v", err)
	}
	if !migrated {
		t.Fatal("non-root .git must be reported as migrated")
	}
	if reclonedTo != repo {
		t.Errorf("re-clone target %q, want %q", reclonedTo, repo)
	}
	entries, _ := filepath.Glob(repo + ".quarantine-*")
	if len(entries) != 1 {
		t.Fatalf("old checkout not kept aside: %v", entries)
	}
	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "jabali:jabali") {
		t.Errorf("no path may be handed to the service user:\n%s", joined)
	}
	if !strings.Contains(joined, "chown -R root:root "+filepath.Join(repo, "panel-ui")) {
		t.Errorf("panel-ui must be root-owned like the rest of the tree:\n%s", joined)
	}
	if !strings.Contains(joined, "chown -R root:root "+filepath.Join(repo, ".git")) {
		t.Errorf(".git must go to root:\n%s", joined)
	}
}

func TestEnforceRepoOwnershipNoCheckoutIsNoop(t *testing.T) {
	migrated, err := enforceRepoOwnership(filepath.Join(t.TempDir(), "absent"),
		func(string, ...string) error { t.Fatal("no command expected"); return nil },
		func(string) error { t.Fatal("no clone expected"); return nil })
	if err != nil || migrated {
		t.Fatalf("fresh host: migrated=%v err=%v", migrated, err)
	}
}

// repair must treat a non-root .git as wrong and re-clone from the canonical
// URL, never from the origin recorded in the existing config.
func TestGitOwnerWrongAcceptsOnlyRoot(t *testing.T) {
	if !gitOwnerWrong("jabali") || gitOwnerWrong("root") {
		t.Fatal("only a root-owned .git may pass the ownership check")
	}
}

func TestRecloneArgsUseCanonicalURL(t *testing.T) {
	args := recloneArgs("/opt/jabali-panel")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, canonicalRepoURL) {
		t.Fatalf("reclone must use canonicalRepoURL, got %q", joined)
	}
	if strings.Contains(joined, "origin") {
		t.Fatalf("reclone must not read origin from the existing config: %q", joined)
	}
}
