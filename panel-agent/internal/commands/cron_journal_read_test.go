package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"os/user"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #2120: a tenant job's output was read with `journalctl --user` run as the
// tenant. That only sees the tenant's own journal file, and journald splits
// one out per user only when the journal is stored persistently. On a host
// with a volatile journal there is none: Run now answered "Exit code 0" with
// no output and the job's log was empty, though the job had run. The output
// is now read as root, limited to the tenant's entries by the journal's
// trusted _UID field.

func currentUserOrSkip(t *testing.T) *user.User {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	return me
}

// journalCall finds the call that read the journal.
func (f *cronExecFake) journalCall() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if (c[0] == "journalctl" || argsHave(c[1:], "journalctl")) && !argsHave(c[1:], "--sync") {
			return c
		}
	}
	return nil
}

func TestCronRunNow_TenantOutputReadAsRootByUID(t *testing.T) {
	me := currentUserOrSkip(t)
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "true",
		showAfter:   "Result=success\nExecMainCode=1\nExecMainStatus=0\nConditionResult=yes",
		journal:     "hello from the job",
	}
	f.install(t)

	out, err := runNowAs(t, context.Background(), map[string]any{"wait_seconds": 5})
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out["output"] != "hello from the job" {
		t.Errorf("output: got %q", out["output"])
	}
	call := f.journalCall()
	if call == nil {
		t.Fatal("the run's output was never read from the journal")
	}
	if call[0] != "journalctl" || argsHave(call[1:], "--user") {
		t.Fatalf("output must be read as root from the whole journal, got %q", call)
	}
	for _, want := range []string{
		"_UID=" + me.Uid,
		"_SYSTEMD_USER_UNIT=jabali-cron-" + runNowTestJobID + ".service",
	} {
		if !argsHave(call[1:], want) {
			t.Errorf("journal read %q lacks match %q", call, want)
		}
	}
}

// tailLogFake records the commands cron.tail_log runs and answers the journal
// read with lines in journalctl's short-iso form.
type tailLogFake struct {
	mu    sync.Mutex
	calls [][]string
}

func (f *tailLogFake) install(t *testing.T) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.mu.Lock()
		f.calls = append(f.calls, append([]string{name}, args...))
		f.mu.Unlock()
		return exec.CommandContext(ctx, "sh", "-c",
			`printf '%s\n' '2026-10-13T23:20:17+0000 host jabali-ssh-shell[42]: hello from the job'`)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func tailLog(t *testing.T, username, jobID string) (*cronTailLogResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"user_id": "u1", "username": username, "job_id": jobID})
	res, err := cronTailLogHandler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	resp, ok := res.(*cronTailLogResponse)
	if !ok {
		t.Fatalf("answer: got %T", res)
	}
	return resp, nil
}

func TestCronTailLog_TenantLogReadAsRootByUID(t *testing.T) {
	me := currentUserOrSkip(t)
	f := &tailLogFake{}
	f.install(t)

	resp, err := tailLog(t, me.Username, runNowTestJobID)
	if err != nil {
		t.Fatalf("tail_log: %v", err)
	}
	if resp.Log != "2026-10-13T23:20:17+0000 hello from the job" {
		t.Errorf("log: got %q", resp.Log)
	}
	if len(f.calls) != 1 {
		t.Fatalf("want one journal read, got %q", f.calls)
	}
	call := f.calls[0]
	if call[0] != "journalctl" || argsHave(call[1:], "--user") {
		t.Fatalf("the log must be read as root from the whole journal, got %q", call)
	}
	unit := "jabali-cron-" + runNowTestJobID + ".service"
	// (owner AND the job's own lines) OR (owner AND the job's identifier, which
	// a last line written without a newline carries alone) OR (owner AND the
	// manager's lines about it).
	want := []string{
		"_UID=" + me.Uid, "_SYSTEMD_USER_UNIT=" + unit, "+",
		"_UID=" + me.Uid, "SYSLOG_IDENTIFIER=jabali-cron-" + runNowTestJobID, "+",
		"_UID=" + me.Uid, "USER_UNIT=" + unit,
	}
	if len(call) < 1+len(want) {
		t.Fatalf("journal read %q: want matches %q first", call, want)
	}
	for i, w := range want {
		if call[1+i] != w {
			t.Fatalf("journal read %q: want matches %q first", call, want)
		}
	}
}

func TestCronTailLog_RejectsAJobIDThatIsNotAULID(t *testing.T) {
	me := currentUserOrSkip(t)
	f := &tailLogFake{}
	f.install(t)

	_, err := tailLog(t, me.Username, "*")
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("want invalid_argument for a job id that is not a ULID, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("nothing may run for a bad job id, ran %q", f.calls)
	}
}
