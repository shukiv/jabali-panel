package commands

// GH #2079: the panel's Rebuild baseline ran aideinit inside the agent call.
// aideinit runs aide as the _aide user, which can't open jabali's report log,
// so aide exited 17; and a rebuild takes minutes, longer than the panel's
// request may last. The agent now starts `jabali aide rebuild --full` in its
// own unit and reports the outcome through security.aide.status.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// aideUnitsExec fakes systemctl/systemd-run. states maps a loaded unit to its
// active state, as `systemctl list-units` shows it; an unlisted unit isn't
// loaded (list-units prints nothing). `systemctl is-active` fails the test:
// on a transient unit that is gone it makes systemd log "Failed to open
// /run/systemd/transient/…" on every status poll (seen on .60).
func aideUnitsExec(t *testing.T, states map[string]string) *[]string {
	t.Helper()
	var cmds []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		cmds = append(cmds, line)
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			t.Errorf("ran %q; read the state with list-units", line)
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
			unit := args[len(args)-1]
			if state, ok := states[unit]; ok {
				return exec.CommandContext(ctx, "echo", unit, "loaded", state, "running", "Jabali", "unit")
			}
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &cmds
}

// aideRebuildExec is aideUnitsExec with the rebuild unit in unitState.
func aideRebuildExec(t *testing.T, unitState string) *[]string {
	t.Helper()
	return aideUnitsExec(t, map[string]string{agentwire.AideRebuildUnit: unitState})
}

func withAideRebuildResult(t *testing.T, r *agentwire.AideRebuildResult) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rebuild.json")
	if r != nil {
		b, _ := json.Marshal(r)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := aideRebuildResultPath
	aideRebuildResultPath = path
	t.Cleanup(func() { aideRebuildResultPath = prev })
}

func TestAideRebuild_StartsTheRebuildInItsOwnUnit(t *testing.T) {
	cmds := aideRebuildExec(t, "inactive")
	withAideRebuildResult(t, nil)

	out, err := mwAideRebuildHandler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := out.(map[string]any); m["started"] != true {
		t.Fatalf("response %v, want started", out)
	}
	var run string
	for _, c := range *cmds {
		if strings.HasPrefix(c, "systemd-run ") {
			run = c
		}
		if strings.Contains(c, "aideinit") {
			t.Fatalf("aideinit runs aide as _aide, which can't write the report log: %q", c)
		}
	}
	for _, want := range []string{
		"--unit=" + agentwire.AideRebuildUnit,
		"--no-block",
		"/usr/local/bin/jabali aide rebuild --full --result-file " + aideRebuildResultPath,
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("systemd-run %q, want %q", run, want)
		}
	}
}

// The agent records the start itself, so a rebuild that dies before the CLI
// writes anything (an old jabali without --result-file, say) still shows as
// stopped instead of vanishing.
func TestAideRebuild_ARebuildThatDiesAtOnceStillShows(t *testing.T) {
	aideRebuildExec(t, "inactive")
	withAideRebuildResult(t, nil)

	if _, err := mwAideRebuildHandler(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	st := readAideRebuildStatus(context.Background())
	if st == nil || st.Running || st.OK == nil || *st.OK || !strings.Contains(st.Error, "stopped before it finished") {
		t.Fatalf("status %+v, want a rebuild that stopped before it finished", st)
	}
	if fi, err := os.Stat(aideRebuildResultPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("result file %v %v, want mode 0600", fi, err)
	}
}

func TestAideRebuild_SaysWhenTheUnitDidNotStart(t *testing.T) {
	withAideRebuildResult(t, nil)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			return exec.CommandContext(ctx, "sh", "-c", "echo 'Failed to start transient service unit: Unit jabali-aide-rebuild.service already exists.' >&2; exit 1")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	_, err := mwAideRebuildHandler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err %v, want systemd-run's reason", err)
	}
	st := readAideRebuildStatus(context.Background())
	if st == nil || st.OK == nil || *st.OK || !strings.Contains(st.Error, "already exists") {
		t.Fatalf("status %+v, want the failed start", st)
	}
}

func TestAideRebuild_RefusesWhileOneIsRunning(t *testing.T) {
	cmds := aideRebuildExec(t, "active")
	withAideRebuildResult(t, nil)

	_, err := mwAideRebuildHandler(context.Background(), json.RawMessage(`{}`))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("err %v, want a failed-precondition agent error", err)
	}
	for _, c := range *cmds {
		if strings.HasPrefix(c, "systemd-run ") {
			t.Fatalf("started a second rebuild: %q", c)
		}
	}
}

func TestAideRebuild_DryRunStartsNothing(t *testing.T) {
	cmds := aideRebuildExec(t, "inactive")
	withAideRebuildResult(t, nil)

	out, err := mwAideRebuildHandler(context.Background(), json.RawMessage(`{"dry_run":true}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.(map[string]any)
	if plan, _ := m["plan"].(string); strings.Contains(plan, "aideinit") || !strings.Contains(plan, "aide --init") {
		t.Fatalf("plan %q, want the aide --init the rebuild runs", plan)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a dry run ran %q", *cmds)
	}
}

func TestAideRebuildStatus(t *testing.T) {
	failed := agentwire.AideRebuildResult{OK: false, Error: "aide --init failed (exit status 18): ERROR: disk full", StartedAt: "2026-10-12T14:00:00Z", FinishedAt: "2026-10-12T14:02:00Z"}
	ok := agentwire.AideRebuildResult{OK: true, StartedAt: "2026-10-12T14:00:00Z", FinishedAt: "2026-10-12T14:02:30Z"}
	running := agentwire.AideRebuildResult{Running: true, StartedAt: "2026-10-12T14:00:00Z"}
	yes := true
	cases := []struct {
		name     string
		unit     string
		file     *agentwire.AideRebuildResult
		wantNil  bool
		running  bool
		ok       *bool
		errWords string
	}{
		{name: "never rebuilt from the panel", unit: "inactive", wantNil: true},
		{name: "running", unit: "active", file: &running, running: true},
		{name: "starting, nothing recorded yet", unit: "activating", running: true},
		{name: "running, the last result not yet replaced", unit: "active", file: &failed, running: true},
		{name: "last rebuild failed", unit: "inactive", file: &failed, ok: new(bool), errWords: "disk full"},
		{name: "last rebuild worked", unit: "inactive", file: &ok, ok: &yes},
		{name: "stopped without recording an end", unit: "inactive", file: &running, ok: new(bool), errWords: "stopped before it finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aideRebuildExec(t, tc.unit)
			withAideRebuildResult(t, tc.file)
			st := readAideRebuildStatus(context.Background())
			if tc.wantNil {
				if st != nil {
					t.Fatalf("status %+v, want none", st)
				}
				return
			}
			if st == nil {
				t.Fatal("no status")
			}
			if st.Running != tc.running {
				t.Fatalf("running %v, want %v", st.Running, tc.running)
			}
			if (st.OK == nil) != (tc.ok == nil) || (st.OK != nil && *st.OK != *tc.ok) {
				t.Fatalf("ok %v, want %v", st.OK, tc.ok)
			}
			if tc.errWords == "" && st.Error != "" {
				t.Fatalf("error %q, want none", st.Error)
			}
			if !strings.Contains(st.Error, tc.errWords) {
				t.Fatalf("error %q, want it to mention %q", st.Error, tc.errWords)
			}
		})
	}
}

// The status call reports the rebuild before any early return (no aide, no
// database), so a first rebuild that failed still says why.
func TestAideStatus_ReportsTheRebuild(t *testing.T) {
	aideRebuildExec(t, "inactive")
	failed := agentwire.AideRebuildResult{Error: "aide --init failed: ERROR: disk full", StartedAt: "2026-10-12T14:00:00Z", FinishedAt: "2026-10-12T14:02:00Z"}
	withAideRebuildResult(t, &failed)
	out, err := mwAideStatusHandler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"rebuild":{`) || !strings.Contains(string(b), "disk full") {
		t.Fatalf("status %s, want the failed rebuild", b)
	}
}

// Every aide run replaces the report log, so after a rebuild it holds the
// --init summary, not a check. That isn't a clean check: the status says the
// baseline was rebuilt and no check has run since.
func TestAideStatus_AnInitInTheReportIsNotACheck(t *testing.T) {
	for name, report := range map[string]string{
		"aide 0.19": "Start timestamp: 2026-10-12 14:32:05 +0000 (AIDE 0.19.4)\nAIDE successfully initialized database.\nNew AIDE database written to /var/lib/aide/aide.db.new\n\nNumber of entries:\t4\n\nEnd timestamp: 2026-10-12 14:34:05 +0000 (run time: 2m 0s)\n",
		"aide 0.18": "Start timestamp: 2026-10-12 14:32:05 +0000 (AIDE 0.18.6)\nAIDE initialized database at /var/lib/aide/aide.db.new\n\nNumber of entries:\t4\n\nEnd timestamp: 2026-10-12 14:34:05 +0000 (run time: 2m 0s)\n",
	} {
		t.Run(name, func(t *testing.T) {
			resp := aideStatusResponse{Sample: []aideSampleRow{}}
			applyAideReport(report, &resp)
			if resp.LastCheckTS != "" || !strings.HasPrefix(resp.BaselineRebuiltAt, "2026-10-12 14:32:05") {
				t.Fatalf("last_check_ts %q baseline_rebuilt_at %q, want no check and the rebuild time", resp.LastCheckTS, resp.BaselineRebuiltAt)
			}
		})
	}
}

func TestAideStatus_ACheckInTheReportIsACheck(t *testing.T) {
	report := "Start timestamp: 2026-10-12 04:30:05 +0000 (AIDE 0.19.4)\nAIDE found differences between database and filesystem!!\n\nSummary:\n  Total number of entries:\t4\n  Added entries:\t\t1\n  Removed entries:\t\t0\n  Changed entries:\t\t1\n\n---------------------------------------------------\nAdded entries:\n---------------------------------------------------\n\nf++++++++++++++++: /watch/c\n\n---------------------------------------------------\nChanged entries:\n---------------------------------------------------\n\nf   ...    . C.. : /watch/a\n"
	resp := aideStatusResponse{Sample: []aideSampleRow{}}
	applyAideReport(report, &resp)
	if !strings.HasPrefix(resp.LastCheckTS, "2026-10-12 04:30:05") || resp.BaselineRebuiltAt != "" {
		t.Fatalf("last_check_ts %q baseline_rebuilt_at %q, want the check", resp.LastCheckTS, resp.BaselineRebuiltAt)
	}
	if resp.Summary.Added != 1 || resp.Summary.Changed != 1 {
		t.Fatalf("summary %+v, want 1 added 1 changed", resp.Summary)
	}
}
