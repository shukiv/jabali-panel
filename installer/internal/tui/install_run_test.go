package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// runInstall drives startInstall the way the model does and returns the log
// lines and the final error, or fails when installDoneMsg never comes.
func runInstall(t *testing.T, script string) ([]string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	events := make(chan tea.Msg, 64)
	first := make(chan tea.Msg, 1)
	go func() { first <- startInstall(path, nil, false, events)() }()

	var lines []string
	deadline := time.After(10 * time.Second)
	next := first
	for {
		select {
		case msg := <-next:
			switch m := msg.(type) {
			case logLineMsg:
				lines = append(lines, string(m))
			case installDoneMsg:
				return lines, m.err
			}
			next = events
		case <-deadline:
			t.Fatalf("installDoneMsg never came; %d lines so far", len(lines))
		}
	}
}

// GH #2590: a child install.sh leaves running holds the output pipe open. The
// screen must still see install.sh exit, with its status, not wait on the child.
func TestStartInstall_ChildHoldingOutputDoesNotHang(t *testing.T) {
	installWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { installWaitDelay = 3 * time.Second })

	for _, tc := range []struct {
		name, exit string
		wantCode   int
	}{
		{"install fails", "exit 3", 3},
		{"install succeeds", "exit 0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			lines, err := runInstall(t, "echo before\nsleep 30 &\necho after >&2\n"+tc.exit+"\n")

			if took := time.Since(start); took > 5*time.Second {
				t.Errorf("took %s: waited on the child, not install.sh", took)
			}
			if !slices.Contains(lines, "before") || !slices.Contains(lines, "after") {
				t.Errorf("lines = %q, want the stdout and the stderr line", lines)
			}
			var ee *exec.ExitError
			switch {
			case tc.wantCode == 0 && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantCode != 0 && (!errors.As(err, &ee) || ee.ExitCode() != tc.wantCode):
				t.Errorf("err = %v, want exit status %d", err, tc.wantCode)
			}
		})
	}
}

// A line longer than the scanner's limit must not stall install.sh: the output
// after it (well past a pipe buffer) still has to drain for it to exit.
func TestStartInstall_OverlongLineDoesNotStall(t *testing.T) {
	long := strings.Repeat("x", 2<<20)
	_, err := runInstall(t, "printf '%s\\n' "+long+"\nfor i in $(seq 1 20000); do echo line$i; done\nexit 0\n")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
