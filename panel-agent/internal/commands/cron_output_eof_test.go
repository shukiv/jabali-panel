package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// GH #2120: the last line a job writes without a trailing newline never
// showed in Run now or in the job's log. journald logs that line only when
// the job's output stream closes, after the job's process has exited, and it
// can then no longer tell which unit the process belonged to: the entry has
// no _SYSTEMD_USER_UNIT (_SYSTEMD_UNIT for a root job), the field the readers
// matched on. A script whose output has no final newline
// (`echo "done<br>";`) showed nothing at all.
//
// The unit now sets SyslogIdentifier=jabali-cron-<id>. journald takes that
// from the output stream itself, so the line keeps it after the process is
// gone, and the readers match on it too, held to the job owner's uid.

// hasSeq reports whether want appears in args as a contiguous run.
func hasSeq(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j, w := range want {
			if args[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestCronUnits_TagTheirOutputWithTheJobsIdentifier(t *testing.T) {
	php := &cronvalidate.Command{Argv: []string{"php8.3", "/home/alice/domains/x/public_html/task.php"}}
	tenant := buildCronServiceContent(runNowTestJobID, "task", []*cronvalidate.Command{php}, "alice", []string{"/home/alice/domains/x/public_html"})
	root := buildRootCronServiceContent(runNowTestJobID, "report", "python3 /root/report.py")

	want := "\nSyslogIdentifier=jabali-cron-" + runNowTestJobID + "\n"
	for name, unit := range map[string]string{"tenant": tenant, "root": root} {
		if !strings.Contains(unit, want) {
			t.Errorf("%s unit must tag its output with the job's identifier:\n%s", name, unit)
		}
		if i, j := strings.Index(unit, "[Service]"), strings.Index(unit, "SyslogIdentifier="); j < i {
			t.Errorf("%s unit: SyslogIdentifier= must be in [Service]:\n%s", name, unit)
		}
	}
}

func TestCronRunNow_ReadsTheLineAJobWritesAsItExits(t *testing.T) {
	me := currentUserOrSkip(t)
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "true",
		showAfter:   "Result=success\nExecMainCode=1\nExecMainStatus=0\nConditionResult=yes",
		journal:     "Flush cache in progress...<br>Cache flushed successfully.",
	}
	f.install(t)

	if _, err := runNowAs(t, context.Background(), map[string]any{"wait_seconds": 5}); err != nil {
		t.Fatalf("run_now: %v", err)
	}
	call := f.journalCall()
	if call == nil {
		t.Fatal("the run's output was never read from the journal")
	}
	owner := "_UID=" + me.Uid
	for _, want := range [][]string{
		{owner, "_SYSTEMD_USER_UNIT=jabali-cron-" + runNowTestJobID + ".service", "+"},
		{"+", owner, "SYSLOG_IDENTIFIER=jabali-cron-" + runNowTestJobID},
	} {
		if !hasSeq(call[1:], want) {
			t.Errorf("journal read %q lacks the term %q", call, want)
		}
	}
	assertNoManagerLines(t, call)
}

// assertNoManagerLines fails when a Run now read would take the manager's
// "Starting…" / "Finished…" lines: the output is the job's own.
func assertNoManagerLines(t *testing.T, call []string) {
	t.Helper()
	for _, a := range call[1:] {
		if strings.HasPrefix(a, "USER_UNIT=") || strings.HasPrefix(a, "UNIT=") {
			t.Errorf("Run now output must not take the manager's lines (%q): %q", a, call)
		}
	}
}

func TestCronRunNow_RootJobReadsTheLineItWritesAsItExits(t *testing.T) {
	f := &cronExecFake{
		showBefore:  "LoadState=loaded\nActiveState=inactive",
		startScript: "true",
		showAfter:   "Result=success\nExecMainCode=1\nExecMainStatus=0\nConditionResult=yes",
		journal:     "report written",
	}
	f.install(t)

	if _, err := rootRunNow(t, nil); err != nil {
		t.Fatalf("run_now: %v", err)
	}
	call := f.journalCall()
	if call == nil {
		t.Fatal("the run's output was never read from the journal")
	}
	for _, want := range [][]string{
		{"_SYSTEMD_UNIT=jabali-cron-" + runNowTestJobID + ".service", "+"},
		{"+", "_UID=0", "SYSLOG_IDENTIFIER=jabali-cron-" + runNowTestJobID},
	} {
		if !hasSeq(call[1:], want) {
			t.Errorf("journal read %q lacks the term %q", call, want)
		}
	}
	assertNoManagerLines(t, call)
}

func TestCronTailLog_RootJobReadsEveryLineOfItsUnit(t *testing.T) {
	f := &cronExecFake{journal: "2026-10-13T00:01:30+0000 host jabali-cron-x[42]: report written"}
	f.install(t)
	raw, _ := json.Marshal(map[string]any{"user_id": "admin1", "username": "root", "job_id": runNowTestJobID, "run_as_root": true})
	if _, err := cronTailLogHandler(context.Background(), raw); err != nil {
		t.Fatalf("cron.tail_log: %v", err)
	}
	unit := "jabali-cron-" + runNowTestJobID + ".service"
	// The unit's own lines, the job's identifier (root's only), and the
	// system manager's lines about the unit: what `journalctl -u` matched,
	// plus the identifier.
	want := "journalctl _SYSTEMD_UNIT=" + unit +
		" + _UID=0 SYSLOG_IDENTIFIER=jabali-cron-" + runNowTestJobID +
		" + _PID=1 UNIT=" + unit +
		" -n 50 -o short-iso --no-pager"
	if got := callLine(f, "journalctl", "-n"); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
