package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// fakeServerInstaller points the installer marker and /proc at temp files:
// process 4242 started at tick 777, and the marker holds marker (no marker
// file when marker is empty).
func fakeServerInstaller(t *testing.T, marker string) {
	t.Helper()
	dir := t.TempDir()
	prevMarker, prevProc := serverInstallerMarker, procDir
	t.Cleanup(func() { serverInstallerMarker, procDir = prevMarker, prevProc })
	serverInstallerMarker = filepath.Join(dir, "jabali-installer.pid")
	procDir = filepath.Join(dir, "proc")
	if err := os.MkdirAll(filepath.Join(procDir, "4242"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A comm with a space and a ")" in it: the fields are counted from the
	// last ")", as the kernel's format needs.
	stat := "4242 (bash) x) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 777 1000 200 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 1 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "4242", "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(serverInstallerMarker, []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// countSystemdRun counts the module installs actually started.
func countSystemdRun(t *testing.T) *int {
	t.Helper()
	n := new(int)
	prev := execCommandContext
	t.Cleanup(func() { execCommandContext = prev })
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			*n++
			return exec.CommandContext(ctx, "true")
		}
		return prev(ctx, name, args...)
	}
	return n
}

// The panel boots in the middle of a full install and its module repair asks
// for mail while install.sh has yet to install Stalwart. The agent refuses
// with the shared answer, starts nothing and records no failure: the running
// installer installs mail itself.
func TestSystemModuleInstall_WaitsForTheServerInstaller(t *testing.T) {
	fakeInstall(t, "", 0)
	fakeServerInstaller(t, "4242 777\n")
	started := countSystemdRun(t)

	raw, _ := json.Marshal(moduleStatusRequest{Key: "mail"})
	_, err := systemModuleInstallHandler(context.Background(), raw)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || ae.Message != agentwire.MsgServerInstallerRunning {
		t.Fatalf("install while the installer runs: err = %v, want %s %q", err, agentwire.CodeFailedPrecondition, agentwire.MsgServerInstallerRunning)
	}
	if *started != 0 {
		t.Errorf("an install was started beside the running installer (%d systemd-run)", *started)
	}
	if st := moduleStatusFor(t, "mail"); st.LastError != "" || st.Installing {
		t.Errorf("status after the refusal: last_error=%q installing=%v, want neither", st.LastError, st.Installing)
	}
}

// A marker the installer left behind doesn't block installs: the process is
// gone, the PID now belongs to a process that started later, or the file is
// not the installer's format.
func TestSystemModuleInstall_AStaleInstallerMarkerDoesNotBlock(t *testing.T) {
	for name, marker := range map[string]string{
		"no marker":        "",
		"process gone":     "4243 777\n",
		"pid reused":       "4242 778\n",
		"not the format":   "4242\n",
		"not numbers":      "../4242 777\n",
		"extra fields":     "4242 777 1\n",
		"negative pid":     "-4242 777\n",
		"start not number": "4242 x\n",
	} {
		t.Run(name, func(t *testing.T) {
			fakeInstall(t, "", 0)
			fakeServerInstaller(t, marker)
			started := countSystemdRun(t)

			raw, _ := json.Marshal(moduleStatusRequest{Key: "mail"})
			if _, err := systemModuleInstallHandler(context.Background(), raw); err != nil {
				t.Fatalf("install: %v", err)
			}
			if *started != 1 {
				t.Errorf("systemd-run ran %d times, want 1", *started)
			}
		})
	}
}
