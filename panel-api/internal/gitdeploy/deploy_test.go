package gitdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// deployAgent answers each command with a fixed answer or error, and
// records what it was asked.
type deployAgent struct {
	calls   []string
	params  []any
	answers map[string]any
	errs    map[string]error
}

func (f *deployAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, command)
	f.params = append(f.params, params)
	if err := f.errs[command]; err != nil {
		return nil, err
	}
	return json.Marshal(f.answers[command])
}

func (f *deployAgent) count(command string) int {
	n := 0
	for _, c := range f.calls {
		if c == command {
			n++
		}
	}
	return n
}

type docRootSet struct{ domainID, userID, docRoot string }

type deployRuns struct {
	repository.DomainGitDeploymentRepository
	saved     []models.DomainGitDeployment
	active    bool
	docRoots  []docRootSet
	changed   bool
	docRootEr error
}

func (f *deployRuns) CreateUnlessRunning(_ context.Context, run *models.DomainGitDeployment) error {
	if f.active {
		return repository.ErrGitDeploymentActive
	}
	f.saved = append(f.saved, *run)
	return nil
}

func (f *deployRuns) Save(_ context.Context, run *models.DomainGitDeployment) error {
	f.saved = append(f.saved, *run)
	return nil
}

func (f *deployRuns) SetDocRoot(_ context.Context, domainID, userID, docRoot string) (bool, error) {
	if f.docRootEr != nil {
		return false, f.docRootEr
	}
	f.docRoots = append(f.docRoots, docRootSet{domainID, userID, docRoot})
	return f.changed, nil
}

type deploySched struct{ scheduled []string }

func (s *deploySched) Schedule(domainID string) { s.scheduled = append(s.scheduled, domainID) }

const (
	testRun     = "01JBX000000000000000000005"
	testRelease = "01JBX000000000000000000003"
	testCommit  = "2f1d6a2c3b4e5f60718293a4b5c6d7e8f9a0b1c2"
)

func newTestRun(kind string) *models.DomainGitDeployment {
	now := time.Now().UTC()
	release := testRun
	if kind == models.GitDeployKindRollback {
		release = testRelease
	}
	return &models.DomainGitDeployment{
		ID: testRun, UserID: "u-alice", DomainID: "d1", OSUser: "alice", Kind: kind, ReleaseID: release,
		DocRoot: DocRoot("alice", "example.com", "public"), State: models.GitDeployRunning,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func TestDocRoot(t *testing.T) {
	if got := DocRoot("alice", "example.com", "public"); got != "/home/alice/domains/example.com/current/public" {
		t.Errorf("DocRoot = %q", got)
	}
	if got := DocRoot("alice", "example.com", ""); got != "/home/alice/domains/example.com/current" {
		t.Errorf("DocRoot of the repository's root = %q", got)
	}
}

func TestStart_SendsTheRunToTheAgent(t *testing.T) {
	ag := &deployAgent{answers: map[string]any{"git.deploy_start": DeployStartResponse{Unit: "u", PHPVersion: "8.3"}}}
	runs := &deployRuns{}
	run := newTestRun(models.GitDeployKindRollback)
	if err := Start(context.Background(), ag, runs, run, DeployStartRequest{Domain: "example.com", WebDir: "public"}); err != nil {
		t.Fatal(err)
	}
	req := ag.params[0].(DeployStartRequest)
	if req.RunID != testRun || req.Kind != "rollback" || req.OSUser != "alice" || req.Release != testRelease {
		t.Errorf("request = %+v", req)
	}
	if run.State != models.GitDeployRunning || run.PHPVersion != "8.3" {
		t.Errorf("run = %+v", run)
	}
}

func TestStart_OneRunPerDomain(t *testing.T) {
	ag := &deployAgent{}
	err := Start(context.Background(), ag, &deployRuns{active: true}, newTestRun(models.GitDeployKindDeploy), DeployStartRequest{})
	if !errors.Is(err, repository.ErrGitDeploymentActive) || len(ag.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, ag.calls)
	}
}

func TestStart_ARefusedRunSaysWhy(t *testing.T) {
	ag := &deployAgent{errs: map[string]error{"git.deploy_start": &agent.AgentError{
		Code: agent.CodeFailedPrecondition, Message: "the hosting package does not include Composer runs"}}}
	run := newTestRun(models.GitDeployKindDeploy)
	err := Start(context.Background(), ag, &deployRuns{}, run, DeployStartRequest{})
	if _, ok := Refusal(err); !ok {
		t.Fatalf("err = %v", err)
	}
	if run.State != models.GitDeployFailed || run.Error != "the hosting package does not include Composer runs" {
		t.Errorf("run = %+v", run)
	}
	if ag.count("git.deploy_stop") != 0 {
		t.Error("a refused run, never started, was stopped")
	}
}

// A start that failed some other way may have started the unit: it is
// stopped, which also removes its credential file.
func TestStart_AnInternalFailureStopsTheRun(t *testing.T) {
	ag := &deployAgent{errs: map[string]error{"git.deploy_start": errors.New("systemd-run: exit status 1: some detail")}}
	run := newTestRun(models.GitDeployKindDeploy)
	_ = Start(context.Background(), ag, &deployRuns{}, run, DeployStartRequest{})
	if ag.count("git.deploy_stop") != 1 {
		t.Fatalf("calls = %v", ag.calls)
	}
	if stop := ag.params[1].(DeployStopRequest); stop.RunID != testRun || stop.OSUser != "alice" {
		t.Errorf("stop = %+v", stop)
	}
	if run.State != models.GitDeployFailed || run.Error != "the server could not start the deployment" {
		t.Errorf("run = %+v", run)
	}
}

func status(state, log string) map[string]any {
	return map[string]any{
		"git.deploy_status": DeployStatusResponse{State: state, Log: log, Commit: testCommit},
		"git.deploy_stop":   DeployStopResponse{},
	}
}

// A deployment that succeeded points the domain at current, has the vhost
// re-applied, and releases its unit.
func TestRefresh_ASuccessMovesTheDocumentRoot(t *testing.T) {
	ag := &deployAgent{answers: status("succeeded", "==> fetching\n"+liveMarker+testRun+"\n")}
	runs := &deployRuns{changed: true}
	sched := &deploySched{}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, runs, sched, run); err != nil {
		t.Fatal(err)
	}
	want := docRootSet{"d1", "u-alice", "/home/alice/domains/example.com/current/public"}
	if len(runs.docRoots) != 1 || runs.docRoots[0] != want {
		t.Errorf("document roots set = %+v", runs.docRoots)
	}
	if len(sched.scheduled) != 1 || sched.scheduled[0] != "d1" {
		t.Errorf("scheduled = %v", sched.scheduled)
	}
	if run.State != models.GitDeploySucceeded || run.CommitSHA != testCommit || len(runs.saved) != 1 {
		t.Errorf("run = %+v, saved %d", run, len(runs.saved))
	}
	if ag.count("git.deploy_stop") != 1 {
		t.Errorf("the unit was not released: %v", ag.calls)
	}
}

// A document root already at current is left as it is, with no vhost
// re-applied.
func TestRefresh_ADocumentRootAlreadyThereIsLeft(t *testing.T) {
	ag := &deployAgent{answers: status("succeeded", liveMarker+testRun+"\n")}
	sched := &deploySched{}
	if err := Refresh(context.Background(), ag, &deployRuns{changed: false}, sched, newTestRun(models.GitDeployKindDeploy)); err != nil {
		t.Fatal(err)
	}
	if len(sched.scheduled) != 0 {
		t.Errorf("scheduled = %v", sched.scheduled)
	}
}

// A failed deployment leaves the document root alone.
func TestRefresh_AFailureLeavesTheDocumentRoot(t *testing.T) {
	ag := &deployAgent{answers: status("failed", "==> fetching\njabali-git-deploy: failed: deploy command 1 failed\n")}
	runs := &deployRuns{changed: true}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, runs, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if len(runs.docRoots) != 0 || run.State != models.GitDeployFailed {
		t.Errorf("run = %+v, document roots = %+v", run, runs.docRoots)
	}
}

// A run whose release went live is a success, whatever else the unit
// reported: the site serves that release.
func TestRefresh_ALiveReleaseIsASuccess(t *testing.T) {
	ag := &deployAgent{answers: status("failed", liveMarker+testRun+"\nsomething after\n")}
	runs := &deployRuns{changed: true}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, runs, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeploySucceeded || len(runs.docRoots) != 1 {
		t.Errorf("run = %+v, document roots = %+v", run, runs.docRoots)
	}
	// A unit lost after its release went live: a success, with no error.
	ag = &deployAgent{answers: status("gone", liveMarker+testRun+"\n")}
	run = newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, &deployRuns{changed: true}, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeploySucceeded || run.Error != "" {
		t.Errorf("a lost run whose release went live = %+v", run)
	}
	// Another release's line doesn't count.
	ag = &deployAgent{answers: status("failed", liveMarker+testRelease+"\n")}
	run = newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, &deployRuns{}, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeployFailed {
		t.Errorf("run = %+v", run)
	}
}

// A unit lost before the panel saw its result is still released: that is
// what removes the run's credential file when the run could not.
func TestRefresh_ALostRunIsReleased(t *testing.T) {
	ag := &deployAgent{answers: status("gone", "")}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, &deployRuns{}, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeployFailed || run.Error == "" {
		t.Errorf("run = %+v", run)
	}
	if ag.count("git.deploy_stop") != 1 {
		t.Fatalf("the lost run was not released: %v", ag.calls)
	}
	if stop := ag.params[1].(DeployStopRequest); stop.OSUser != "alice" {
		t.Errorf("stop = %+v", stop)
	}
}

// When the document root can't be written the run is not stored as
// finished, and its unit is kept: the next refresh tries again.
func TestRefresh_ADocumentRootNotWrittenIsTriedAgain(t *testing.T) {
	ag := &deployAgent{answers: status("succeeded", liveMarker+testRun+"\n")}
	runs := &deployRuns{docRootEr: errors.New("db down")}
	if err := Refresh(context.Background(), ag, runs, &deploySched{}, newTestRun(models.GitDeployKindDeploy)); err == nil {
		t.Fatal("Refresh succeeded")
	}
	if len(runs.saved) != 0 || ag.count("git.deploy_stop") != 0 {
		t.Errorf("saved %d, calls %v", len(runs.saved), ag.calls)
	}
}

func TestRefresh_StillRunning(t *testing.T) {
	ag := &deployAgent{answers: status("running", "==> fetching\n")}
	runs := &deployRuns{}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Refresh(context.Background(), ag, runs, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeployRunning || run.Log != "==> fetching\n" || len(runs.saved) != 0 || ag.count("git.deploy_stop") != 0 {
		t.Errorf("run = %+v, saved %d, calls %v", run, len(runs.saved), ag.calls)
	}
}

// A run stopped after its release went live is stored as succeeded, and
// the domain follows the release.
func TestCancel_AfterTheReleaseWentLive(t *testing.T) {
	ag := &deployAgent{answers: status("running", liveMarker+testRun+"\n")}
	runs := &deployRuns{changed: true}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Cancel(context.Background(), ag, runs, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeploySucceeded || len(runs.docRoots) != 1 {
		t.Errorf("run = %+v, document roots = %+v", run, runs.docRoots)
	}
}

func TestCancel(t *testing.T) {
	ag := &deployAgent{answers: status("running", "==> fetching\n")}
	runs := &deployRuns{changed: true}
	run := newTestRun(models.GitDeployKindDeploy)
	if err := Cancel(context.Background(), ag, runs, &deploySched{}, run); err != nil {
		t.Fatal(err)
	}
	if run.State != models.GitDeployCancelled || len(runs.docRoots) != 0 || len(runs.saved) != 1 {
		t.Errorf("run = %+v, document roots = %+v, saved %d", run, runs.docRoots, len(runs.saved))
	}
	run.State = models.GitDeploySucceeded
	if err := Cancel(context.Background(), ag, runs, &deploySched{}, run); !errors.Is(err, ErrNotRunning) {
		t.Errorf("cancelling a finished run: %v", err)
	}
}

func TestValidateCommands(t *testing.T) {
	got, err := ValidateCommands("php artisan migrate --force\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "php artisan migrate --force" {
		t.Errorf("normalized = %q", got)
	}
	if _, err := ValidateCommands("sh -c id"); err == nil {
		t.Error("a shell was accepted")
	}
}

// The panel's side of the git.deploy_* and git.releases contract: the
// agent's TestGitDeploy_PanelContract decodes the same fixtures.
func TestDeployContract(t *testing.T) {
	const dir = "../agent/testdata/"
	roundTrip[DeployStartRequest](t, dir+"git_deploy_start_request.json")
	roundTrip[DeployStartResponse](t, dir+"git_deploy_start_response.json")
	roundTrip[DeployStatusResponse](t, dir+"git_deploy_status_response.json")
	roundTrip[DeployStopRequest](t, dir+"git_deploy_stop_request.json")
	roundTrip[ReleasesRequest](t, dir+"git_releases_request.json")
	roundTrip[ReleasesResponse](t, dir+"git_releases_response.json")
}
