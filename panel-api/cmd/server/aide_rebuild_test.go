package main

// GH #2079: `jabali aide rebuild --full` ran Debian's aideinit, which runs
// aide as the _aide user. _aide can't open jabali's report_url
// (/var/log/aide/aide.report.log, written as root by the daily check), so aide
// exited 17 and the reason went only to /var/log/aide/aideinit.errors: the
// command printed "aideinit: exit status 17" and nothing else.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// fakeAide points the rebuild at a shell script standing in for aide, and the
// database paths at a temp dir. script gets $ARGS_FILE and $DB_NEW.
func fakeAide(t *testing.T, script string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	bin := filepath.Join(dir, "aide")
	body := "#!/bin/sh\nARGS_FILE=" + filepath.Join(dir, "args") + "\nDB_NEW=" + filepath.Join(dir, "aide.db.new") + "\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aide.db"), []byte("old baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := [4]string{aideBinary, aideConfigPath, aideDBFile, aideDBNewFile}
	aideBinary = bin
	aideConfigPath = "/etc/aide/aide.conf"
	aideDBFile = filepath.Join(dir, "aide.db")
	aideDBNewFile = filepath.Join(dir, "aide.db.new")
	t.Cleanup(func() { aideBinary, aideConfigPath, aideDBFile, aideDBNewFile = prev[0], prev[1], prev[2], prev[3] })
	return dir
}

const aideDeniedScript = `echo "  ERROR: /etc/aide/aide.conf:11: open (read/write) failed for file '/var/log/aide/aide.report.log': Permission denied (line: 'report_url=file:/var/log/aide/aide.report.log')" >&2
exit 17`

const aideOKScript = `echo "$@" > "$ARGS_FILE"
printf 'new baseline' > "$DB_NEW"
echo "AIDE successfully initialized database."`

func TestRebuildAideBaseline_SaysWhyAideFailed(t *testing.T) {
	dir := fakeAide(t, aideDeniedScript)
	_, err := rebuildAideBaseline(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "Permission denied") || !strings.Contains(err.Error(), "aide.report.log") {
		t.Fatalf("error %q doesn't say why aide failed", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "aide.db")); string(b) != "old baseline" {
		t.Fatalf("the baseline changed on a failed rebuild: %q", b)
	}
}

func TestRebuildAideBaseline_RunsAideAndSwapsTheDatabase(t *testing.T) {
	dir := fakeAide(t, aideOKScript)
	if _, err := rebuildAideBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if strings.TrimSpace(string(args)) != "--init --config=/etc/aide/aide.conf" {
		t.Fatalf("aide ran with %q", args)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "aide.db"))
	if string(b) != "new baseline" {
		t.Fatalf("aide.db = %q, want the new baseline", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "aide.db")); st.Mode().Perm() != 0o600 {
		t.Fatalf("aide.db mode %v, want 0600", st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "aide.db.new")); !os.IsNotExist(err) {
		t.Fatal("aide.db.new is left behind")
	}
}

func runAideRebuildCmd(t *testing.T, resultFile string) error {
	t.Helper()
	cmd := newAideRebuildCmd()
	cmd.SetArgs([]string{"--full", "--result-file", resultFile})
	cmd.SetOut(&strings.Builder{})
	return cmd.Execute()
}

func readRebuildResult(t *testing.T, path string) agentwire.AideRebuildResult {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r agentwire.AideRebuildResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAideRebuildCmd_RecordsAFailureInTheResultFile(t *testing.T) {
	dir := fakeAide(t, aideDeniedScript)
	result := filepath.Join(dir, "result.json")
	if err := runAideRebuildCmd(t, result); err == nil {
		t.Fatal("want the command to fail")
	}
	r := readRebuildResult(t, result)
	if r.Running || r.OK || !strings.Contains(r.Error, "Permission denied") || r.StartedAt == "" || r.FinishedAt == "" {
		t.Fatalf("result %+v, want a finished failure with aide's reason", r)
	}
}

func TestAideRebuildCmd_RecordsASuccessInTheResultFile(t *testing.T) {
	dir := fakeAide(t, aideOKScript)
	result := filepath.Join(dir, "result.json")
	if err := runAideRebuildCmd(t, result); err != nil {
		t.Fatal(err)
	}
	r := readRebuildResult(t, result)
	if r.Running || !r.OK || r.Error != "" || r.FinishedAt == "" {
		t.Fatalf("result %+v, want a finished success", r)
	}
}

// GH #2079: security.aide.check now starts jabali-aide-check.service and
// returns at once, so `jabali aide check` waits for the check to end and then
// prints the status, as it printed the check's result before.
type fakeAideAgent struct {
	checkErr error
	states   []string // check state per status call: "running", "failed", "done"
	calls    []string
}

func (f *fakeAideAgent) call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	f.calls = append(f.calls, command)
	switch command {
	case "security.aide.check":
		if f.checkErr != nil {
			return nil, f.checkErr
		}
		return json.RawMessage(`{"started":true}`), nil
	case "security.aide.status":
		state := "done"
		if n := len(f.calls) - 2; n < len(f.states) {
			state = f.states[n]
		}
		switch state {
		case "running":
			return json.RawMessage(`{"enabled":true,"check":{"running":true}}`), nil
		case "failed":
			return json.RawMessage(`{"enabled":true,"check":{"running":false,"failed":true}}`), nil
		}
		return json.RawMessage(`{"enabled":true,"last_check_ts":"2026-10-12 15:00:00 +0000","summary":{"added":0,"changed":2,"removed":0}}`), nil
	}
	return nil, errors.New("unexpected command " + command)
}

func fastAideCheckPoll(t *testing.T) {
	t.Helper()
	prevPoll, prevGrace := aideCheckPoll, aideCheckStartGrace
	aideCheckPoll, aideCheckStartGrace = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { aideCheckPoll, aideCheckStartGrace = prevPoll, prevGrace })
}

func TestAideCheckCmd_WaitsForTheCheckAndPrintsTheStatus(t *testing.T) {
	fastAideCheckPoll(t)
	f := &fakeAideAgent{states: []string{"running", "running"}}
	var out strings.Builder
	if err := runAideCheck(context.Background(), f.call, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"changed":2`) {
		t.Fatalf("output %q, want the status after the check", out.String())
	}
	if len(f.calls) < 4 {
		t.Fatalf("calls %q, want the check and a status poll until it ended", f.calls)
	}
}

func TestAideCheckCmd_SaysWhenTheCheckFailed(t *testing.T) {
	fastAideCheckPoll(t)
	f := &fakeAideAgent{states: []string{"running", "failed"}}
	err := runAideCheck(context.Background(), f.call, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "journalctl -u jabali-aide-check.service") {
		t.Fatalf("err %v, want the failed check and where to look", err)
	}
}

func TestAideCheckCmd_SaysWhyTheCheckDidNotStart(t *testing.T) {
	fastAideCheckPoll(t)
	f := &fakeAideAgent{checkErr: errors.New("a check is already running")}
	err := runAideCheck(context.Background(), f.call, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err %v, want the agent's reason", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls %q, want no status poll", f.calls)
	}
}
