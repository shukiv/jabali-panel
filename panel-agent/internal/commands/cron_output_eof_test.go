package commands

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// GH #2120: the last line a tenant job writes without a trailing newline
// never showed in Run now or in the job's log. journald logs that line only
// when the job's output stream closes, after the job has exited. It then
// can't find the unit through the writer's cgroup, and it takes the unit
// name a stream announces only from root, so for a tenant job the entry has
// no _SYSTEMD_USER_UNIT, the field the readers matched on. A script whose
// output has no final newline (`echo "done<br>";`) showed nothing at all.
// (A root job's line keeps _SYSTEMD_UNIT, so its reads are unchanged.)
//
// The tenant unit now sets SyslogIdentifier=jabali-cron-<id>, which travels
// with the output stream, and the readers match on it too, held to the
// tenant's uid.

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

func TestCronUnit_TagsItsOutputWithTheJobsIdentifier(t *testing.T) {
	php := &cronvalidate.Command{Argv: []string{"php8.3", "/home/alice/domains/x/public_html/task.php"}}
	unit := buildCronServiceContent(runNowTestJobID, "task", []*cronvalidate.Command{php}, "alice", []string{"/home/alice/domains/x/public_html"})

	if !strings.Contains(unit, "\nSyslogIdentifier=jabali-cron-"+runNowTestJobID+"\n") {
		t.Fatalf("the unit must tag its output with the job's identifier:\n%s", unit)
	}
	if i, j := strings.Index(unit, "[Service]"), strings.Index(unit, "SyslogIdentifier="); j < i {
		t.Fatalf("SyslogIdentifier= must be in [Service]:\n%s", unit)
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
