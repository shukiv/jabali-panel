package diagnostic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMain makes the diagnostic test suite hermetic: unless the operator opts
// into real host access (JABALI_ALLOW_HOST_MUTATION=1), every exec routed
// through the package seam is redirected to a harmless shell stub that drains
// stdin, emits nothing, and exits 0. The collectors shell out to read-only host
// tools (systemctl status, journalctl, iptables -L, cscli list, cat, tail);
// this keeps a bare `go test ./...` from reaching them, so the bundle tests are
// deterministic and don't depend on what the CI host happens to be running.
// GH #994 / #1160. The env-var name is shared with the commands seam so one
// opt-in re-enables real exec across the agent.
func TestMain(m *testing.M) {
	if os.Getenv("JABALI_ALLOW_HOST_MUTATION") == "1" {
		os.Exit(m.Run())
	}

	dir, err := os.MkdirTemp("", "jabali-diag-stub-")
	if err != nil {
		panic("diagnostic exec stub: " + err.Error())
	}
	stub := filepath.Join(dir, "noexec")
	// Path baked into argv (not an env marker) so a collector that sets cmd.Env
	// can't defeat it; `cat >/dev/null` drains any piped stdin.
	if werr := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null 2>&1\nexit 0\n"), 0o700); werr != nil {
		panic("diagnostic exec stub: " + werr.Error())
	}

	// Set once, before any test runs → race-free.
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
