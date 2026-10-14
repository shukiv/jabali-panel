package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os/user"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-293 root jobs (run_as_root) are installed as SYSTEM units under
// /etc/systemd/system. Run now, the log and the Last run harvest looked for
// them in a user's manager instead (the owning admin's account, which usually
// does not exist: Run now and the log answered 502), and the unit appended its
// output to /var/log/jabali/cron/root/<id>.log, a directory nothing created,
// so every scheduled run failed with 209/STDOUT.

func rootRunNow(t *testing.T, extra map[string]any) (map[string]any, error) {
	t.Helper()
	params := map[string]any{
		"user_id":     "admin1",
		"username":    "root",
		"job_id":      runNowTestJobID,
		"command":     "python3 /root/report.py",
		"run_as_root": true,
	}
	for k, v := range extra {
		params[k] = v
	}
	raw, _ := json.Marshal(params)
	res, err := cronRunNowHandler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, nil
}

func callLine(f *cronExecFake, name, arg string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c[0] == name && argsHave(c[1:], arg) {
			return strings.Join(c, " ")
		}
	}
	return ""
}

func TestCronRunNow_RootJobRunsThroughItsSystemUnit(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "exec sleep 30",
	}
	f.install(t)

	out, err := rootRunNow(t, map[string]any{"wait_seconds": 1})
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out["running"] != true {
		t.Fatalf("a root run that outlives the wait must be reported as running; got %v", out)
	}
	want := "systemctl start jabali-cron-" + runNowTestJobID + ".service"
	if got := callLine(f, "systemctl", "start"); got != want {
		t.Fatalf("start: got %q want %q (the system unit, not a user manager)", got, want)
	}
	if f.ran("sudo", "") || f.ran("runuser", "") {
		t.Fatalf("a root job with a system unit must not go through a user account; calls=%q", f.calls)
	}
}

func TestCronRunNow_RootJobOutputComesFromTheSystemJournal(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "true",
		showAfter:   "Result=success\nExecMainCode=1\nExecMainStatus=0\nConditionResult=yes",
		journal:     "report written",
	}
	f.install(t)

	out, err := rootRunNow(t, nil)
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out["exit_code"] != float64(0) || out["output"] != "report written" {
		t.Fatalf("got %v, want exit 0 and the journal output", out)
	}
	if got := callLine(f, "journalctl", "_SYSTEMD_UNIT=jabali-cron-"+runNowTestJobID+".service"); got == "" {
		t.Fatalf("output must be read from the system journal by _SYSTEMD_UNIT; calls=%q", f.calls)
	}
}

func TestCronRunNow_RunAsRootRequiresTheRootAccount(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	f := &cronExecFake{showBefore: "LoadState=loaded\nActiveState=inactive", startScript: "true"}
	f.install(t)
	// A command valid for that account, so only the account mismatch refuses it.
	_, err = rootRunNow(t, map[string]any{"username": me.Username, "command": "python3 /home/" + me.Username + "/report.py"})
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("run_as_root with another account must be refused; got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing may run; calls=%q", f.calls)
	}
}

func TestCronStatus_RootJobReadsItsSystemUnit(t *testing.T) {
	f := &cronExecFake{showBefore: "ExecMainStartTimestamp=Mon 2026-10-12 23:26:56 UTC\nExecMainStatus=4\nActiveState=failed"}
	f.install(t)
	raw, _ := json.Marshal(map[string]any{"user_id": "admin1", "username": "root", "job_id": runNowTestJobID, "run_as_root": true})
	res, err := cronStatusHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("cron.status: %v", err)
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["has_run"] != true || out["exit_code"] != float64(4) {
		t.Fatalf("got %v, want the system unit's last run (exit 4)", out)
	}
	if got := callLine(f, "systemctl", "show"); !strings.HasPrefix(got, "systemctl show jabali-cron-"+runNowTestJobID+".service") {
		t.Fatalf("status must read the system unit; calls=%q", f.calls)
	}
}

func TestCronTailLog_RootJobReadsTheSystemJournal(t *testing.T) {
	f := &cronExecFake{journal: "2026-10-13T00:01:30+0000 host jabali-cron-x[42]: report written"}
	f.install(t)
	raw, _ := json.Marshal(map[string]any{"user_id": "admin1", "username": "root", "job_id": runNowTestJobID, "run_as_root": true})
	res, err := cronTailLogHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("cron.tail_log: %v", err)
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["log"] != "2026-10-13T00:01:30+0000 report written" {
		t.Errorf("log: got %q", out["log"])
	}
	want := "journalctl -u jabali-cron-" + runNowTestJobID + ".service -n 50 -o short-iso --no-pager"
	if got := callLine(f, "journalctl", "-u"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// Only a job's own unit: the job id must be a ULID.
	g := &cronExecFake{}
	g.install(t)
	raw, _ = json.Marshal(map[string]any{"user_id": "admin1", "username": "root", "job_id": "ssh", "run_as_root": true})
	_, err = cronTailLogHandler(context.Background(), raw)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument || len(g.calls) != 0 {
		t.Fatalf("a non-ULID job id must be refused before reading any journal; err=%v calls=%q", err, g.calls)
	}
}

func TestBuildRootCronService_LogsToTheJournal(t *testing.T) {
	unit := buildRootCronServiceContent(runNowTestJobID, "report", "python3 /root/report.py")
	if strings.Contains(unit, "StandardOutput=") || strings.Contains(unit, "StandardError=") || strings.Contains(unit, "/var/log/jabali/cron/root") {
		t.Fatalf("the root unit must log to the journal (no file output in a directory nothing creates):\n%s", unit)
	}
	if !strings.Contains(unit, "ExecStart=/bin/sh -c 'python3 /root/report.py'") {
		t.Fatalf("unit lost its command:\n%s", unit)
	}
}
