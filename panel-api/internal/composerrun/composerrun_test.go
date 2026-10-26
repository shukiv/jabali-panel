package composerrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeAgent answers each composer.* command with a canned answer or error
// and records the calls.
type fakeAgent struct {
	answers map[string]any
	errs    map[string]error
	calls   []string
	params  map[string]any
}

func (f *fakeAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, command)
	if f.params == nil {
		f.params = map[string]any{}
	}
	f.params[command] = params
	if err := f.errs[command]; err != nil {
		return nil, err
	}
	return json.Marshal(f.answers[command])
}

// fakeRuns keeps runs in memory.
type fakeRuns struct {
	repository.ComposerRunRepository
	runs   map[string]models.ComposerRun
	active bool
	saves  int
}

func (f *fakeRuns) CreateUnlessRunning(_ context.Context, run *models.ComposerRun) error {
	if f.active {
		return repository.ErrComposerRunActive
	}
	if f.runs == nil {
		f.runs = map[string]models.ComposerRun{}
	}
	f.runs[run.ID] = *run
	return nil
}

func (f *fakeRuns) Save(_ context.Context, run *models.ComposerRun) error {
	f.saves++
	f.runs[run.ID] = *run
	return nil
}

func newRun() *models.ComposerRun {
	return &models.ComposerRun{ID: "01JCXMPSRRVN00000000000AB1", DomainID: "dom", UserID: "usr", Command: "install"}
}

func TestValidate(t *testing.T) {
	for _, ok := range []struct {
		cmd    string
		pkgs   []string
		noDev  bool
		appDir string
	}{
		{"install", nil, true, ""},
		{"update", []string{"monolog/monolog", "symfony/console"}, false, "app"},
		{"require", []string{"monolog/monolog:^3.0 || ^2.9", "a-b/c.d"}, false, "app/sub"},
		{"remove", []string{"monolog/monolog"}, false, ""},
		{"audit", nil, true, ""},
		{"dump-autoload", nil, true, ""},
		{"diagnose", nil, false, ""},
	} {
		if err := Validate(ok.cmd, ok.pkgs, ok.noDev, ok.appDir); err != nil {
			t.Errorf("%s %v: %v", ok.cmd, ok.pkgs, err)
		}
	}
	tooMany := make([]string, MaxPackages+1)
	for i := range tooMany {
		tooMany[i] = "a/b"
	}
	for _, bad := range []struct {
		name   string
		cmd    string
		pkgs   []string
		noDev  bool
		appDir string
	}{
		{"unknown command", "exec", nil, false, ""},
		{"option as command", "--working-dir=/", nil, false, ""},
		{"no_dev where there is none", "validate", nil, true, ""},
		{"require with nothing", "require", nil, false, ""},
		{"packages on install", "install", []string{"a/b"}, false, ""},
		{"option as package", "require", []string{"--dev"}, false, ""},
		{"bare name", "require", []string{"monolog"}, false, ""},
		{"constraint with $", "require", []string{"a/b:$HOME"}, false, ""},
		{"constraint with %", "require", []string{"a/b:%h"}, false, ""},
		{"constraint on update", "update", []string{"a/b:^1"}, false, ""},
		{"too many packages", "remove", tooMany, false, ""},
		{"app dir with ..", "install", nil, false, "app/../.."},
		{"absolute app dir", "install", nil, false, "/etc"},
		{"app dir with a space", "install", nil, false, "my app"},
	} {
		if err := Validate(bad.cmd, bad.pkgs, bad.noDev, bad.appDir); err == nil {
			t.Errorf("%s: accepted", bad.name)
		}
	}
}

func TestStart_SendsTheRunToTheAgent(t *testing.T) {
	ag := &fakeAgent{answers: map[string]any{"composer.run_start": StartResponse{Unit: "u", PHPVersion: "8.5"}}}
	runs := &fakeRuns{}
	run := newRun()
	run.Command, run.AppDir, run.NoDev = "require", "app", false
	run.SetPackageList([]string{"monolog/monolog:^3.0"})
	if err := Start(context.Background(), ag, runs, run, "alice", "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := StartRequest{RunID: run.ID, OSUser: "alice", Domain: "example.com", AppDir: "app", Command: "require", Packages: []string{"monolog/monolog:^3.0"}}
	if got := ag.params["composer.run_start"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("run_start params = %+v, want %+v", got, want)
	}
	if got := runs.runs[run.ID]; got.State != models.ComposerRunRunning || got.PHPVersion != "8.5" {
		t.Fatalf("stored run = %+v", got)
	}
}

// One run at a time per domain: a second start stores nothing and never
// reaches the agent.
func TestStart_OneRunPerDomain(t *testing.T) {
	ag := &fakeAgent{}
	runs := &fakeRuns{active: true}
	if err := Start(context.Background(), ag, runs, newRun(), "alice", "example.com"); !errors.Is(err, repository.ErrComposerRunActive) {
		t.Fatalf("Start = %v, want ErrComposerRunActive", err)
	}
	if len(ag.calls) != 0 || len(runs.runs) != 0 {
		t.Fatalf("calls=%q runs=%v", ag.calls, runs.runs)
	}
}

// A run the agent refuses is stored as failed with the agent's message,
// which is written for the user.
func TestStart_ARefusedRunSaysWhy(t *testing.T) {
	refusal := &agent.AgentError{Code: agent.CodeFailedPrecondition, Message: "example.com does not run PHP: enable PHP on the domain first"}
	ag := &fakeAgent{errs: map[string]error{"composer.run_start": refusal}}
	runs := &fakeRuns{}
	run := newRun()
	err := Start(context.Background(), ag, runs, run, "alice", "example.com")
	if ae, ok := Refusal(err); !ok || ae.Code != agent.CodeFailedPrecondition {
		t.Fatalf("Start = %v, want the refusal", err)
	}
	got := runs.runs[run.ID]
	if got.State != models.ComposerRunFailed || got.Error != refusal.Message || got.FinishedAt == nil {
		t.Fatalf("stored run = %+v", got)
	}
	if !reflect.DeepEqual(ag.calls, []string{"composer.run_start"}) {
		t.Errorf("calls = %q: stopped a run the agent never started", ag.calls)
	}
}

// An internal failure may leave a unit running: it is stopped. The agent's
// message (systemd-run's stderr) stays out of the run (JAB-114).
func TestStart_AnInternalFailureStopsTheUnitAndKeepsTheDetailOut(t *testing.T) {
	ag := &fakeAgent{
		errs:    map[string]error{"composer.run_start": &agent.AgentError{Code: agent.CodeInternal, Message: "systemd-run: exit status 1: Failed to start transient service unit: /run/systemd/x"}},
		answers: map[string]any{"composer.run_stop": StopResponse{}},
	}
	runs := &fakeRuns{}
	run := newRun()
	if err := Start(context.Background(), ag, runs, run, "alice", "example.com"); err == nil {
		t.Fatal("Start succeeded")
	}
	if !reflect.DeepEqual(ag.calls, []string{"composer.run_start", "composer.run_stop"}) {
		t.Fatalf("calls = %q, want the unit stopped", ag.calls)
	}
	got := runs.runs[run.ID]
	if got.State != models.ComposerRunFailed || strings.Contains(got.Error, "systemd") || got.Error == "" {
		t.Fatalf("stored error = %q", got.Error)
	}
}

func TestRefresh(t *testing.T) {
	zero, two := 0, 2
	for _, c := range []struct {
		name     string
		status   StatusResponse
		state    string
		exit     *int
		stored   bool
		released bool
		errText  string
	}{
		{"still running", StatusResponse{State: "running", Log: "Loading"}, models.ComposerRunRunning, nil, false, false, ""},
		{"succeeded", StatusResponse{State: "succeeded", Result: "success", ExitCode: &zero, Log: "done"}, models.ComposerRunSucceeded, &zero, true, true, ""},
		{"failed", StatusResponse{State: "failed", Result: "exit-code", ExitCode: &two, Log: "err"}, models.ComposerRunFailed, &two, true, true, ""},
		{"timed out", StatusResponse{State: "timed_out", Result: "timeout"}, models.ComposerRunTimedOut, nil, true, true, ""},
		{"unit gone", StatusResponse{State: "gone", Log: "half"}, models.ComposerRunFailed, nil, true, false, "restarted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ag := &fakeAgent{answers: map[string]any{"composer.run_status": c.status, "composer.run_stop": StopResponse{}}}
			run := newRun()
			run.State = models.ComposerRunRunning
			runs := &fakeRuns{runs: map[string]models.ComposerRun{run.ID: *run}}
			if err := Refresh(context.Background(), ag, runs, run); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			if run.State != c.state || !reflect.DeepEqual(run.ExitCode, c.exit) || run.Log != c.status.Log {
				t.Fatalf("run = %s exit=%v log=%q", run.State, run.ExitCode, run.Log)
			}
			if (runs.saves > 0) != c.stored {
				t.Errorf("stored=%v, want %v", runs.saves > 0, c.stored)
			}
			released := len(ag.calls) == 2 && ag.calls[1] == "composer.run_stop"
			if released != c.released {
				t.Errorf("calls = %q, released=%v want %v", ag.calls, released, c.released)
			}
			if c.errText != "" && !strings.Contains(run.Error, c.errText) {
				t.Errorf("error = %q", run.Error)
			}
			if c.stored && run.FinishedAt == nil {
				t.Error("a finished run has no finished_at")
			}
		})
	}

	// A blocked run is stored as blocked.
	ag := &fakeAgent{answers: map[string]any{"composer.run_status": StatusResponse{State: "failed", PHPDefenseBlocked: true}, "composer.run_stop": StopResponse{}}}
	run := newRun()
	run.State = models.ComposerRunRunning
	runs := &fakeRuns{runs: map[string]models.ComposerRun{run.ID: *run}}
	if err := Refresh(context.Background(), ag, runs, run); err != nil || !runs.runs[run.ID].PHPDefenseBlocked {
		t.Fatalf("blocked run: err=%v stored=%+v", err, runs.runs[run.ID])
	}

	// A finished run is not asked about again.
	done := newRun()
	done.State = models.ComposerRunSucceeded
	ag = &fakeAgent{}
	if err := Refresh(context.Background(), ag, &fakeRuns{}, done); err != nil || len(ag.calls) != 0 {
		t.Fatalf("finished run: err=%v calls=%q", err, ag.calls)
	}
}

func TestCancel(t *testing.T) {
	ag := &fakeAgent{answers: map[string]any{
		"composer.run_status": StatusResponse{State: "running", Log: "Downloading"},
		"composer.run_stop":   StopResponse{},
	}}
	run := newRun()
	run.State = models.ComposerRunRunning
	runs := &fakeRuns{runs: map[string]models.ComposerRun{run.ID: *run}}
	if err := Cancel(context.Background(), ag, runs, run); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := runs.runs[run.ID]; got.State != models.ComposerRunCancelled || got.Log != "Downloading" || got.FinishedAt == nil {
		t.Fatalf("stored = %+v", got)
	}
	if !reflect.DeepEqual(ag.calls, []string{"composer.run_status", "composer.run_stop", "composer.run_status"}) {
		t.Fatalf("calls = %q", ag.calls)
	}

	// A run that finished meanwhile keeps its own result.
	zero := 0
	ag = &fakeAgent{answers: map[string]any{
		"composer.run_status": StatusResponse{State: "succeeded", Result: "success", ExitCode: &zero},
		"composer.run_stop":   StopResponse{},
	}}
	run = newRun()
	run.State = models.ComposerRunRunning
	runs = &fakeRuns{runs: map[string]models.ComposerRun{run.ID: *run}}
	if err := Cancel(context.Background(), ag, runs, run); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Cancel = %v, want ErrNotRunning", err)
	}
	if got := runs.runs[run.ID]; got.State != models.ComposerRunSucceeded {
		t.Fatalf("stored = %s, want succeeded", got.State)
	}
}

// roundTrip decodes a fixture into T with every field kept. The agent's
// handler test decodes the same fixtures (composer_runs_test.go), so a field
// renamed on either side fails one of them.
func roundTrip[T any](t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var typed T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&typed); err != nil {
		t.Fatalf("decode %s into %T: %v", path, typed, err)
	}
	again, _ := json.Marshal(typed)
	var got, want any
	_ = json.Unmarshal(again, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%T drops or renames fields:\nwant %s\ngot  %s", typed, raw, again)
	}
}

func TestContract(t *testing.T) {
	const dir = "../agent/testdata/"
	roundTrip[DetectRequest](t, dir+"composer_detect_request.json")
	roundTrip[DetectResponse](t, dir+"composer_detect_response.json")
	roundTrip[StartRequest](t, dir+"composer_run_start_request.json")
	roundTrip[StartResponse](t, dir+"composer_run_start_response.json")
	roundTrip[RunIDRequest](t, dir+"composer_run_id_request.json")
	roundTrip[StatusResponse](t, dir+"composer_run_status_response.json")
	roundTrip[StopResponse](t, dir+"composer_run_stop_response.json")
}
