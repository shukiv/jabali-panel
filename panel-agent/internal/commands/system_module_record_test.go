package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// GH #2056: a failed module install only reached the panel log, so the Modules
// card showed "installing" for five minutes and then "not installed" with no
// reason. The agent now keeps the last install result per module and
// system.module.status reports it.

// The shapes below are what install.sh --install-module printed on a reporter's
// server (hostname replaced).
const (
	dieOutput = "\x1b[1;34m[i]\x1b[0m install log: /var/log/jabali/install-2026-10-08_09-12-03.log (includes every step + wrapped command output)\n" +
		"\x1b[1;31m[✗]\x1b[0m install.sh --install-module mail: the DNS module must be installed first (pdns self-zone for 'panel.example.com' not found). Enable DNS, then mail.\n"
	systemdRunOutput = "Failed to start transient service unit: Transaction for jabali-module-install-dns.service/start is destructive (reboot.target has 'start' job queued, but 'stop' is included in transaction).\n"
	diedOutput       = "\x1b[1;34m[i]\x1b[0m install log: /var/log/jabali/install-2026-10-08_12-21-05.log (includes every step + wrapped command output)\n" +
		"\x1b[1;31m[✗]\x1b[0m a non-fatal error (_err) printed before the crash\n" +
		"\x1b[1;31m[jabali-install]\x1b[0m install.sh died:\n" +
		"    exit_code : 1\n" +
		"    function  : install_powerdns\n" +
		"    line      : 4733\n" +
		"    command   : systemctl restart pdns\n" +
		"    trace     :\n" +
		"        install_powerdns() at line 4733\n" +
		"\n[diagnostic] journalctl -u pdns -n 20 --no-pager:\n" +
		"    Oct 08 12:21:11 host pdns_server[44720]: Fatal error: Trying to set unknown setting 'x'\n" +
		"\nFull install log at: /var/log/jabali/install-2026-10-08_12-21-05.log\n" +
		"[diagnostic] last 30 lines of install log:\n" +
		"    [✗] an earlier, non-fatal error\n"
)

func TestSummarizeModuleInstallFailure(t *testing.T) {
	for _, tc := range []struct {
		name, out, wantSummary, wantLog string
	}{
		{
			name:        "install.sh _die line",
			out:         dieOutput,
			wantSummary: "the DNS module must be installed first (pdns self-zone for 'panel.example.com' not found). Enable DNS, then mail.",
			wantLog:     "/var/log/jabali/install-2026-10-08_09-12-03.log",
		},
		{
			name:        "systemd-run refused to start the unit",
			out:         systemdRunOutput,
			wantSummary: "Failed to start transient service unit: Transaction for jabali-module-install-dns.service/start is destructive (reboot.target has 'start' job queued, but 'stop' is included in transaction).",
		},
		{
			name:        "install.sh died block wins over earlier [✗] lines and the log tail",
			out:         diedOutput,
			wantSummary: "install.sh stopped in install_powerdns (line 4733) at: systemctl restart pdns",
			wantLog:     "/var/log/jabali/install-2026-10-08_12-21-05.log",
		},
		{
			name:        "no output",
			out:         "",
			wantSummary: "install failed without output",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary, logPath := summarizeModuleInstallFailure(tc.out)
			if summary != tc.wantSummary {
				t.Errorf("summary = %q\nwant      %q", summary, tc.wantSummary)
			}
			if logPath != tc.wantLog {
				t.Errorf("log = %q, want %q", logPath, tc.wantLog)
			}
			if strings.Contains(summary, "\x1b") {
				t.Errorf("summary keeps terminal escapes: %q", summary)
			}
		})
	}

	// A runaway line is capped.
	long, _ := summarizeModuleInstallFailure(strings.Repeat("x", 5000))
	if len(long) > moduleInstallSummaryMax+len("…") {
		t.Errorf("summary is %d bytes, want at most %d", len(long), moduleInstallSummaryMax)
	}
}

// fakeInstall points the module install at a temp install.sh and a temp record
// dir, and makes `systemd-run` print out and exit with rc.
func fakeInstall(t *testing.T, out string, rc int) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	prevScript, prevDir, prevExec := moduleInstallScript, moduleInstallStateDir, execCommandContext
	t.Cleanup(func() {
		moduleInstallScript, moduleInstallStateDir, execCommandContext = prevScript, prevDir, prevExec
	})
	moduleInstallScript = script
	moduleInstallStateDir = filepath.Join(dir, "module-install")
	outFile := filepath.Join(dir, "out")
	if err := os.WriteFile(outFile, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			return exec.CommandContext(ctx, "sh", "-c", `cat "$1"; exit "$2"`, "sh", outFile, strconv.Itoa(rc))
		}
		return prevExec(ctx, name, args...)
	}
	return moduleInstallStateDir
}

func moduleStatusFor(t *testing.T, key string) moduleStatusResponse {
	t.Helper()
	raw, _ := json.Marshal(moduleStatusRequest{Key: key})
	got, err := systemModuleStatusHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("status %s: %v", key, err)
	}
	return got.(moduleStatusResponse)
}

func TestSystemModuleInstall_TheFailureIsReportedByStatus(t *testing.T) {
	stateDir := fakeInstall(t, dieOutput, 1)
	raw, _ := json.Marshal(moduleStatusRequest{Key: "mail"})
	if _, err := systemModuleInstallHandler(context.Background(), raw); err == nil {
		t.Fatal("install: want the failure returned to the caller as before")
	}

	st := moduleStatusFor(t, "mail")
	if st.LastError != "the DNS module must be installed first (pdns self-zone for 'panel.example.com' not found). Enable DNS, then mail." {
		t.Errorf("last_error = %q", st.LastError)
	}
	if st.InstallLog != "/var/log/jabali/install-2026-10-08_09-12-03.log" {
		t.Errorf("install_log = %q", st.InstallLog)
	}
	if st.LastErrorAt == "" {
		t.Error("last_error_at is empty")
	}
	if st.Installing {
		t.Error("installing = true after the install returned")
	}

	// The record survives an agent restart: it's a file, readable only by root.
	fi, err := os.Stat(filepath.Join(stateDir, "mail.json"))
	if err != nil {
		t.Fatalf("record file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %o, want 600", fi.Mode().Perm())
	}

	// Another module's status is unaffected.
	if other := moduleStatusFor(t, "dns"); other.LastError != "" {
		t.Errorf("dns last_error = %q, want none", other.LastError)
	}
}

func TestSystemModuleInstall_ASuccessClearsTheError(t *testing.T) {
	fakeInstall(t, dieOutput, 1)
	raw, _ := json.Marshal(moduleStatusRequest{Key: "mail"})
	_, _ = systemModuleInstallHandler(context.Background(), raw)

	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			return exec.CommandContext(ctx, "true")
		}
		return prevExec(ctx, name, args...)
	}
	if _, err := systemModuleInstallHandler(context.Background(), raw); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if st := moduleStatusFor(t, "mail"); st.LastError != "" || st.InstallLog != "" {
		t.Errorf("after a successful install: last_error=%q install_log=%q, want none", st.LastError, st.InstallLog)
	}
}

func TestSystemModuleInstall_AMissingInstallShIsRecorded(t *testing.T) {
	fakeInstall(t, "", 0)
	moduleInstallScript = filepath.Join(t.TempDir(), "absent", "install.sh")
	raw, _ := json.Marshal(moduleStatusRequest{Key: "dns"})
	if _, err := systemModuleInstallHandler(context.Background(), raw); err == nil {
		t.Fatal("want an error for a missing install.sh")
	}
	if st := moduleStatusFor(t, "dns"); !strings.Contains(st.LastError, "install.sh missing") {
		t.Errorf("last_error = %q, want the missing install.sh", st.LastError)
	}
}

// A recorded failure stops being reported once the module is installed and
// running (for example, an operator fixed it by hand).
func TestMergeModuleInstallRecord(t *testing.T) {
	rec := &moduleInstallRecord{OK: false, Error: "boom", Log: "/var/log/jabali/x.log", FinishedAt: "2026-10-09T10:00:00Z"}

	down := moduleStatusResponse{Key: "dns"}
	mergeModuleInstallRecord(&down, rec)
	if down.LastError != "boom" || down.InstallLog != "/var/log/jabali/x.log" || down.LastErrorAt != "2026-10-09T10:00:00Z" {
		t.Errorf("down module: %+v, want the recorded failure", down)
	}

	up := moduleStatusResponse{Key: "dns", Installed: true, Active: true}
	mergeModuleInstallRecord(&up, rec)
	if up.LastError != "" || up.InstallLog != "" || up.LastErrorAt != "" {
		t.Errorf("running module: %+v, want no stale error", up)
	}

	ok := moduleStatusResponse{Key: "dns"}
	mergeModuleInstallRecord(&ok, &moduleInstallRecord{OK: true, FinishedAt: "2026-10-09T10:00:00Z"})
	if ok.LastError != "" || ok.LastErrorAt != "" {
		t.Errorf("successful record: %+v, want no error", ok)
	}
	mergeModuleInstallRecord(&ok, nil)
	if ok.LastError != "" {
		t.Errorf("no record: %+v", ok)
	}
}

// installing reads the transient unit, so an install that outlives an agent
// restart, or sits in "activating" after a reboot, still shows as running.
func TestSystemModuleStatus_InstallingWhileTheUnitRuns(t *testing.T) {
	fakeInstall(t, "", 0)
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemctl" && len(args) == 2 && args[0] == "is-active" && args[1] == "jabali-module-install-dns" {
			return exec.CommandContext(ctx, "echo", "activating")
		}
		return prevExec(ctx, name, args...)
	}
	if st := moduleStatusFor(t, "dns"); !st.Installing {
		t.Error("installing = false while jabali-module-install-dns is activating")
	}
	if st := moduleStatusFor(t, "mail"); st.Installing {
		t.Error("mail installing = true, but only the dns unit runs")
	}

	// An install queued behind another one (waiting on aptMu) counts too.
	moduleInstallBegin("mail")
	if st := moduleStatusFor(t, "mail"); !st.Installing {
		t.Error("installing = false while the mail install is queued")
	}
	moduleInstallEnd("mail")
	if st := moduleStatusFor(t, "mail"); st.Installing {
		t.Error("installing = true after the mail install ended")
	}
}

// The install handler itself marks the module as installing from the moment
// it is called until it returns, so the card shows it while the install runs.
func TestSystemModuleInstall_StatusShowsItWhileItRuns(t *testing.T) {
	fakeInstall(t, "", 0)
	release := filepath.Join(t.TempDir(), "release")
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			return exec.CommandContext(ctx, "sh", "-c", `while [ ! -e "$1" ]; do sleep 0.01; done`, "sh", release)
		}
		return prevExec(ctx, name, args...)
	}
	raw, _ := json.Marshal(moduleStatusRequest{Key: "security"})
	done := make(chan error, 1)
	go func() {
		_, err := systemModuleInstallHandler(context.Background(), raw)
		done <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !moduleStatusFor(t, "security").Installing {
		if time.Now().After(deadline) {
			t.Fatal("installing = false while the security install runs")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("install: %v", err)
	}
	if moduleStatusFor(t, "security").Installing {
		t.Error("installing = true after the install returned")
	}
}
