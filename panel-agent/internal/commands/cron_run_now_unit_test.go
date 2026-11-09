package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #2122: Run now ran the job as a child of the agent call, so the call's
// deadline killed a job that ran past it. These tests drive cron.run_now with
// a faked exec layer: a tenant job with an installed unit must run through
// that unit, and a run that outlives the wait must be reported as still
// running, not killed and not waited on past the call.
//
// The tests read the handler's answer as JSON, so they also build against the
// old handler and fail there.

const runNowTestJobID = "01JRVNNMW0000000000000TEST"

// cronExecFake stands in for execCommandContext. The first `systemctl --user
// show` answers showBefore, later ones showAfter.
type cronExecFake struct {
	showBefore, showAfter string
	showFails             bool
	startScript           string
	journal               string

	mu    sync.Mutex
	calls [][]string
	shows int
}

func (f *cronExecFake) install(t *testing.T) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.mu.Lock()
		f.calls = append(f.calls, append([]string{name}, args...))
		script, arg := "true", ""
		// A tenant job's commands go through setpriv (or sudo where it is
		// missing) to the user's manager; a root job's call
		// systemctl/journalctl directly.
		manager := name == "sudo" || name == "systemctl" || name == cronSetpriv
		switch {
		case argsHave(args, "--sync"):
		case manager && argsHave(args, "show"):
			f.shows++
			script, arg = `printf '%s\n' "$1"`, f.showBefore
			if f.shows > 1 {
				arg = f.showAfter
			}
			if f.showFails {
				script = "echo 'Failed to connect to bus' >&2; exit 1"
			}
		case manager && argsHave(args, "start"):
			script = f.startScript
		case name == "journalctl" || (name == "sudo" && argsHave(args, "journalctl")):
			script, arg = `printf '%s\n' "$1"`, f.journal
		case name == "runuser":
			script = "echo via-runuser"
		}
		f.mu.Unlock()
		return exec.CommandContext(ctx, "sh", "-c", script, "sh", arg)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func (f *cronExecFake) ran(name, arg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c[0] == name && (arg == "" || argsHave(c[1:], arg)) {
			return true
		}
	}
	return false
}

func argsHave(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func (f *cronExecFake) ranExactly(cmdline string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Join(c, " ") == cmdline {
			return true
		}
	}
	return false
}

// cronUserManagerArgv is how a tenant job's systemctl reaches u's manager:
// setpriv, which switches to the tenant without a PAM session or a log line.
func cronUserManagerArgv(u *user.User) []string {
	return []string{cronSetpriv, "--reuid=" + u.Uid, "--regid=" + u.Gid, "--init-groups", "--",
		"env", "-i", "PATH=" + cronUserPath,
		"XDG_RUNTIME_DIR=/run/user/" + u.Uid,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + u.Uid + "/bus",
		"systemctl", "--user"}
}

// fakeSetpriv points cronSetpriv at a file that exists (present) or doesn't,
// so a test doesn't depend on the host's /usr/bin/setpriv.
func fakeSetpriv(t *testing.T, present bool) {
	t.Helper()
	old := cronSetpriv
	cronSetpriv = filepath.Join(t.TempDir(), "setpriv")
	if present {
		if err := os.WriteFile(cronSetpriv, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { cronSetpriv = old })
}

func cronStatusAs(t *testing.T, me *user.User) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"user_id": "u1", "username": me.Username, "job_id": runNowTestJobID})
	if _, err := cronStatusHandler(context.Background(), raw); err != nil {
		t.Fatalf("cron.status: %v", err)
	}
}

// The reconciler reads every tenant job's status each pass. sudo -u logged
// three journal lines per read (PAM session opened and closed, and the
// command); setpriv switches to the tenant without either.
func TestCronStatus_ReadsTheUnitThroughSetpriv(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	f := &cronExecFake{showBefore: "ExecMainStartTimestamp=Mon 2026-10-12 23:26:56 UTC\nExecMainStatus=0\nActiveState=inactive"}
	f.install(t)
	fakeSetpriv(t, true)

	cronStatusAs(t, me)

	want := strings.Join(append(cronUserManagerArgv(me), "show", "jabali-cron-"+runNowTestJobID+".service",
		"--no-pager", "-p", "ExecMainStartTimestamp", "-p", "ExecMainStatus", "-p", "ActiveState"), " ")
	if !f.ranExactly(want) {
		t.Fatalf("expected %q among calls %q", want, f.calls)
	}
	if f.ran("sudo", "") {
		t.Fatalf("a status read went through sudo: %q", f.calls)
	}
}

// Without setpriv the tenant's manager is still reached, through sudo.
func TestCronStatus_FallsBackToSudoWithoutSetpriv(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	f := &cronExecFake{showBefore: "ExecMainStartTimestamp=Mon 2026-10-12 23:26:56 UTC\nExecMainStatus=0\nActiveState=inactive"}
	f.install(t)
	fakeSetpriv(t, false)

	cronStatusAs(t, me)

	want := strings.Join([]string{"sudo", "-u", me.Username, "env",
		"XDG_RUNTIME_DIR=/run/user/" + me.Uid,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + me.Uid + "/bus",
		"systemctl", "--user", "show", "jabali-cron-" + runNowTestJobID + ".service",
		"--no-pager", "-p", "ExecMainStartTimestamp", "-p", "ExecMainStatus", "-p", "ActiveState"}, " ")
	if !f.ranExactly(want) {
		t.Fatalf("expected %q among calls %q", want, f.calls)
	}
}

// runNowAs calls cron.run_now for a job of the current user whose command
// passes validation, and returns the answer decoded as JSON.
func runNowAs(t *testing.T, ctx context.Context, extra map[string]any) (map[string]any, error) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	docroot := "/home/" + me.Username + "/runnow.test/public_html"
	params := map[string]any{
		"user_id":        "u1",
		"username":       me.Username,
		"job_id":         runNowTestJobID,
		"command":        "php " + docroot + "/import.php",
		"owned_docroots": []string{docroot},
	}
	for k, v := range extra {
		params[k] = v
	}
	raw, _ := json.Marshal(params)
	res, err := cronRunNowHandler(ctx, raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	return out, nil
}

func TestCronRunNow_LongRunKeepsGoingInItsUnit(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "exec sleep 30",
	}
	f.install(t)
	fakeSetpriv(t, true)

	began := time.Now()
	out, err := runNowAs(t, context.Background(), map[string]any{"wait_seconds": 1})
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if took := time.Since(began); took > 6*time.Second {
		t.Fatalf("run_now waited %s for a run that outlives a 1 s wait", took)
	}
	if out["running"] != true {
		t.Fatalf("a run that outlives the wait must be reported as running; got %v", out)
	}
	if f.ran("runuser", "") {
		t.Fatal("a job with an installed unit must run through the unit, not as a child of the call")
	}
	me, _ := user.Current()
	wantStart := strings.Join(append(cronUserManagerArgv(me),
		"start", "jabali-cron-"+runNowTestJobID+".service"), " ")
	if !f.ranExactly(wantStart) {
		t.Fatalf("expected %q among calls %q", wantStart, f.calls)
	}
}

func TestCronRunNow_FinishedRunReportsExitCodeAndOutput(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=failed",
		startScript: "echo 'Job for jabali-cron-x.service failed' >&2; exit 1",
		showAfter:   "Result=exit-code\nExecMainCode=1\nExecMainStatus=3\nConditionResult=yes",
		journal:     "importing\nTraceback: boom",
	}
	f.install(t)

	out, err := runNowAs(t, context.Background(), map[string]any{"wait_seconds": 5})
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out["running"] == true {
		t.Fatalf("a finished run is not running: %v", out)
	}
	if out["exit_code"] != float64(3) {
		t.Errorf("exit_code: got %v want 3 (the command's own status)", out["exit_code"])
	}
	if out["output"] != "importing\nTraceback: boom" {
		t.Errorf("output: got %q want the run's journal lines", out["output"])
	}
	if f.ran("runuser", "") {
		t.Error("the run went through the unit; nothing may run it a second time")
	}
}

func TestCronRunNow_AlreadyRunningStartsNothing(t *testing.T) {
	f := &cronExecFake{showBefore: "LoadState=loaded\nActiveState=activating"}
	f.install(t)

	out, err := runNowAs(t, context.Background(), nil)
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out["already_running"] != true {
		t.Fatalf("a running unit must answer already_running; got %v", out)
	}
	if f.ran("sudo", "start") || f.ran("runuser", "") {
		t.Fatal("nothing may start while the job is already running")
	}
}

func TestCronRunNow_WithoutAUnitRunsTheCommandDirectly(t *testing.T) {
	for name, f := range map[string]*cronExecFake{
		"unit not installed yet": {showBefore: "LoadState=not-found\nActiveState=inactive"},
		"user manager down":      {showFails: true},
	} {
		t.Run(name, func(t *testing.T) {
			f.install(t)
			out, err := runNowAs(t, context.Background(), nil)
			if err != nil {
				t.Fatalf("run_now: %v", err)
			}
			if !f.ran("runuser", "") || f.ran("sudo", "start") {
				t.Fatalf("expected the direct runuser path; calls=%q", f.calls)
			}
			if out["stdout"] != "via-runuser" {
				t.Errorf("stdout: got %v", out["stdout"])
			}
		})
	}
}

func TestCronRunNow_StartRefusedIsAnError(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "echo 'Start request repeated too quickly.' >&2; exit 1",
		showAfter:   "Result=start-limit-hit\nExecMainCode=0\nExecMainStatus=0\nConditionResult=yes",
	}
	f.install(t)

	_, err := runNowAs(t, context.Background(), nil)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("a start systemd refused must be an error, not a run result; got %v", err)
	}
	if !strings.Contains(ae.Message, "repeated too quickly") {
		t.Errorf("error should carry systemctl's reason; got %q", ae.Message)
	}
}

func TestCronRunNow_SkippedByMissingSiteFolder(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "true",
		showAfter:   "Result=success\nExecMainCode=0\nExecMainStatus=0\nConditionResult=no",
	}
	f.install(t)

	out, err := runNowAs(t, context.Background(), nil)
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	stderr, _ := out["stderr"].(string)
	if out["exit_code"] == float64(0) || !strings.Contains(stderr, "did not run") {
		t.Fatalf("a run its condition skipped must not look like a success; got %v", out)
	}
}

func TestCronRunNow_RejectsAJobIDThatIsNotAULID(t *testing.T) {
	f := &cronExecFake{}
	f.install(t)
	_, err := runNowAs(t, context.Background(), map[string]any{"job_id": "x.service ssh"})
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing may run for a bad job id; calls=%q", f.calls)
	}
}

// cron.status feeds the reconciler's Last run harvest, which only writes a
// newer timestamp. A unit that is still running already shows its start time
// and ExecMainStatus=0; reporting that would record "exit 0" for an unfinished
// run, and the real result (same start time) would never replace it.
func TestCronStatus_RunInProgressIsNotALastRun(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	for _, tc := range []struct {
		state      string
		wantHasRun bool
	}{
		{"activating", false},
		{"inactive", true},
		{"failed", true},
	} {
		t.Run(tc.state, func(t *testing.T) {
			f := &cronExecFake{showBefore: "ExecMainStartTimestamp=Mon 2026-10-12 23:26:56 UTC\nExecMainStatus=0\nActiveState=" + tc.state}
			f.install(t)
			raw, _ := json.Marshal(map[string]any{"user_id": "u1", "username": me.Username, "job_id": runNowTestJobID})
			res, err := cronStatusHandler(context.Background(), raw)
			if err != nil {
				t.Fatalf("cron.status: %v", err)
			}
			b, _ := json.Marshal(res)
			var out map[string]any
			_ = json.Unmarshal(b, &out)
			if (out["has_run"] == true) != tc.wantHasRun {
				t.Fatalf("ActiveState=%s: has_run=%v want %v", tc.state, out["has_run"], tc.wantHasRun)
			}
		})
	}
}
