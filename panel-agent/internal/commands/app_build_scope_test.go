package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// scopeExec records every command runAsUserInSlice starts: systemd-run runs
// run (a shell snippet), systemctl only logs.
func scopeExec(t *testing.T, run string) (calls func() []string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.log")
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		appendLine(t, log, name+" "+strings.Join(args, " "))
		if name == "systemd-run" {
			return exec.CommandContext(ctx, "sh", "-c", run)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

var scopeUnitRe = regexp.MustCompile(`--unit=(jabali-app-build-01APP-[0-9]+\.scope) `)

func TestRunAsUserInSlice_RunsInTheSliceAndStopsTheScope(t *testing.T) {
	calls := scopeExec(t, "echo built")
	out, err := runAsUserInSlice(context.Background(), "01APP", "alice", "/home/alice/app", "/usr/bin/npm", "ci")
	if err != nil || out != "built\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	got := calls()
	if len(got) != 2 {
		t.Fatalf("calls = %q", got)
	}
	m := scopeUnitRe.FindStringSubmatch(got[0])
	if m == nil {
		t.Fatalf("no build scope in %q", got[0])
	}
	want := "systemd-run --scope --quiet --collect --slice=jabali-user-alice.slice --unit=" + m[1] +
		" --property=TimeoutStopSec=5 -- sudo -u alice -H /usr/bin/env -C /home/alice/app -- /usr/bin/npm ci"
	if got[0] != want {
		t.Fatalf("argv = %q, want %q", got[0], want)
	}
	// Whatever the command left running ends with it.
	if got[1] != "systemctl stop "+m[1] {
		t.Fatalf("after the command: %q, want the scope stopped", got[1])
	}

	// No directory: the user's process starts where sudo does.
	calls = scopeExec(t, "true")
	if _, err := runAsUserInSlice(context.Background(), "01APP", "alice", "", "/srv/venv/bin/pip", "install", "gunicorn"); err != nil {
		t.Fatal(err)
	}
	if got := calls()[0]; !strings.HasSuffix(got, " -- sudo -u alice -H /srv/venv/bin/pip install gunicorn") {
		t.Fatalf("argv = %q", got)
	}
}

// A cancelled build command stops its scope, the whole process tree, and
// returns at once.
func TestRunAsUserInSlice_CancelStopsTheScope(t *testing.T) {
	calls := scopeExec(t, "exec sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	if _, err := runAsUserInSlice(ctx, "01APP", "alice", "/home/alice/app", "/usr/bin/npm", "ci"); err == nil {
		t.Fatal("a cancelled command must return an error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancelled command returned after %v", d)
	}
	got := calls()
	m := scopeUnitRe.FindStringSubmatch(got[0])
	if m == nil || len(got) < 2 || got[1] != "systemctl stop "+m[1] {
		t.Fatalf("calls = %q, want the scope stopped on cancel", got)
	}
}

func TestStopAppBuildScopes_EveryScopeOfTheApp(t *testing.T) {
	calls := scopeExec(t, "true")
	stopAppBuildScopes("01APP")
	if got := calls(); len(got) != 1 || got[0] != "systemctl stop jabali-app-build-01APP-*.scope" {
		t.Fatalf("calls = %q", got)
	}
}

// Removing an app ends its build commands too, also one a previous agent
// started.
func TestPythonAppRemove_StopsTheBuildScopes(t *testing.T) {
	calls := scopeExec(t, "true")
	if _, err := pythonAppRemoveHandler(context.Background(), []byte(`{"app_id":"01APP"}`)); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls() {
		if c == "systemctl stop jabali-app-build-01APP-*.scope" {
			return
		}
	}
	t.Fatalf("calls = %q, want the app's build scopes stopped", calls())
}
