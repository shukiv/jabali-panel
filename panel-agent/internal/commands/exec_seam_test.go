package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMain makes the commands test suite structurally incapable of mutating the
// host. Unless the operator opts into real host mutation
// (JABALI_ALLOW_HOST_MUTATION=1), every exec routed through the package seam is
// redirected to a harmless shell stub that drains stdin, emits nothing, and
// exits 0 — no systemctl, no timedatectl, no rm, no polkit prompt. This is the
// real cure for GH #994; the per-test requireHostMutationAllowed gates remain as
// belt-and-suspenders and compose with it (they skip before reaching exec).
func TestMain(m *testing.M) {
	if os.Getenv("JABALI_ALLOW_HOST_MUTATION") == "1" {
		// Integration path: real exec, real host. The per-test gates decide what
		// actually runs.
		os.Exit(m.Run())
	}

	dir, err := os.MkdirTemp("", "jabali-exec-stub-")
	if err != nil {
		panic("exec seam stub: " + err.Error())
	}
	stub := filepath.Join(dir, "noexec")
	// The stub path is baked into argv (below), NOT into an env marker, so the
	// 28 handlers that set cmd.Env cannot defeat it. `cat >/dev/null` drains any
	// piped stdin so handlers that set cmd.Stdin do not see EPIPE.
	script := "#!/bin/sh\ncat >/dev/null 2>&1\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		panic("exec seam stub: " + err.Error())
	}

	// Set once, before any test runs → race-free (no testMutex needed).
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, stub, append([]string{name}, args...)...)
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(stub, append([]string{name}, args...)...)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// withRealExec restores the real os/exec dispatch for the duration of a single
// NON-PARALLEL test. Use it ONLY for a test that must run a genuinely read-only
// command (du, id, getent, sshd -t) to be meaningful — the stub's empty output
// would otherwise break it. NEVER use it for a test that could mutate the host;
// gate those with requireHostMutationAllowed instead. Safe without a mutex
// because Go pauses t.Parallel tests until the sequential phase (where these
// run) completes. GH #994.
func withRealExec(t *testing.T) {
	t.Helper()
	prevCtx, prevCmd := execCommandContext, execCommand
	execCommandContext = exec.CommandContext
	execCommand = exec.Command
	t.Cleanup(func() {
		execCommandContext = prevCtx
		execCommand = prevCmd
	})
}

// TestExecSeam_NoRawExec keeps the cure durable: no non-test file in package
// commands may call os/exec directly. New destructive handlers must route
// through the execCommand / execCommandContext seam, which the stub above
// neutralizes in tests. GH #994.
func TestExecSeam_NoRawExec(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\bexec\.Command(Context)?\(`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue // test scaffolding (incl. this file's stub redirect) may use raw exec
		}
		if name == "exec_seam.go" {
			continue // the seam itself defines the vars from os/exec
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if loc := re.FindIndex(b); loc != nil {
			line := 1 + strings.Count(string(b[:loc[0]]), "\n")
			t.Errorf("%s:%d: raw exec.Command/CommandContext — route through the execCommand/execCommandContext seam instead (GH #994)", name, line)
		}
	}
}
