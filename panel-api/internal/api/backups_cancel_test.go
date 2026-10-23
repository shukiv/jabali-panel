package api

// GH #2245: Cancel on the admin Backups page answered "Cancellation
// requested" but stopped nothing: backup.cancel was a no-op on the agent and
// the handler never touched the row, so the backup ran on and stayed
// "running". The handler now seals the row cancelled once nothing runs.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type cancelJobsRepo struct {
	repository.BackupJobRepository
	job        *models.BackupJob
	sealResult bool
	sealed     int
	sealText   string
}

func (r *cancelJobsRepo) Get(_ context.Context, id string) (*models.BackupJob, error) {
	if r.job == nil || r.job.ID != id {
		return nil, repository.ErrNotFound
	}
	return r.job, nil
}

func (r *cancelJobsRepo) MarkCancelledIfActive(_ context.Context, _ string, errText string) (bool, error) {
	r.sealed++
	r.sealText = errText
	return r.sealResult, nil
}

type cancelAgent struct {
	reply string
	err   error
	rpc   string
	calls int
}

func (a *cancelAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	a.calls++
	a.rpc = method
	return json.RawMessage(a.reply), a.err
}

const cancelTestJobID = "01HZ2245CANCE000000000000A"

func runCancel(t *testing.T, system bool, jobs *cancelJobsRepo, ag *cancelAgent) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/"+cancelTestJobID+"/cancel", nil)
	c.Params = gin.Params{{Key: "job_id", Value: cancelTestJobID}}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Agent: ag}}
	if system {
		h.systemCancel(c)
	} else {
		h.cancel(c)
	}
	return rec
}

func cancelTestJob(status, kind string) *models.BackupJob {
	return &models.BackupJob{ID: cancelTestJobID, Kind: kind, Status: status}
}

func TestCancel_RunningJobStopped_SealsCancelled(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"job_id":"` + cancelTestJobID + `","stopped":true,"job_state":"running"}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if ag.rpc != "backup.cancel" {
		t.Errorf("rpc = %q, want backup.cancel", ag.rpc)
	}
	if jobs.sealed != 1 || jobs.sealText != cancelledByAdminText {
		t.Fatalf("sealed %d times with %q, want once with %q", jobs.sealed, jobs.sealText, cancelledByAdminText)
	}
}

func TestSystemCancel_UsesTheSystemRPC(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindSystemBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"running"}`}

	rec := runCancel(t, true, jobs, ag)

	if rec.Code != http.StatusOK || ag.rpc != "system.backup_cancel" || jobs.sealed != 1 {
		t.Fatalf("code=%d rpc=%q sealed=%d, want 200, system.backup_cancel, sealed once", rec.Code, ag.rpc, jobs.sealed)
	}
}

// The agent has no record of the job (it died with a previous agent
// process): nothing runs, so the row is sealed.
func TestCancel_RunningJobTheAgentLost_SealsCancelled(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":false,"job_state":""}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusOK || jobs.sealed != 1 {
		t.Fatalf("code=%d sealed=%d, want 200 and sealed", rec.Code, jobs.sealed)
	}
}

// The backup finished a moment before the cancel reached the agent. Its
// row must keep the real outcome, which the finalizer records.
func TestCancel_JobFinishedOnTheAgent_KeepsTheOutcome(t *testing.T) {
	for _, state := range []string{"succeeded", "failed"} {
		jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
		ag := &cancelAgent{reply: `{"stopped":false,"job_state":"` + state + `"}`}

		rec := runCancel(t, false, jobs, ag)

		if rec.Code != http.StatusConflict || errorCode(t, rec) != "job_already_finished" {
			t.Errorf("%s: code=%d error=%q, want 409 job_already_finished", state, rec.Code, errorCode(t, rec))
		}
		if jobs.sealed != 0 {
			t.Errorf("%s: a finished backup was relabelled cancelled", state)
		}
	}
}

// An agent from before GH #2245 stops nothing; the row must not claim it did.
func TestCancel_OlderAgent_RefusesToSeal(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"job_id":"` + cancelTestJobID + `","cancel":"noop_v1"}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusBadGateway || errorCode(t, rec) != "agent_cannot_cancel" || jobs.sealed != 0 {
		t.Fatalf("code=%d error=%q sealed=%d, want 502 agent_cannot_cancel, not sealed", rec.Code, errorCode(t, rec), jobs.sealed)
	}
}

func TestCancel_RunningJobAgentUnreachable_DoesNotSeal(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{err: errors.New("dial unix /run/jabali/agent.sock: connect: no such file or directory")}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusBadGateway || jobs.sealed != 0 {
		t.Fatalf("code=%d sealed=%d, want 502 and not sealed", rec.Code, jobs.sealed)
	}
}

// A queued job has not reached the agent; it is cancelled even when the
// agent can't be asked.
func TestCancel_QueuedJob_SealsWithoutTheAgent(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusQueued, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{err: errors.New("agent down")}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusOK || jobs.sealed != 1 {
		t.Fatalf("code=%d sealed=%d, want 200 and sealed", rec.Code, jobs.sealed)
	}
}

func TestCancel_FinishedJob_Conflict(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusSucceeded, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":false}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusConflict || errorCode(t, rec) != "job_not_active" {
		t.Fatalf("code=%d error=%q, want 409 job_not_active", rec.Code, errorCode(t, rec))
	}
	if ag.calls != 0 || jobs.sealed != 0 {
		t.Errorf("agent calls=%d sealed=%d, want neither for a finished job", ag.calls, jobs.sealed)
	}
}

func TestCancel_RestoreJob_Conflict(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountRestore), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":false}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusConflict || errorCode(t, rec) != "not_cancellable" || jobs.sealed != 0 {
		t.Fatalf("code=%d error=%q sealed=%d, want 409 not_cancellable", rec.Code, errorCode(t, rec), jobs.sealed)
	}
}

func TestCancel_UnknownJob_NotFound(t *testing.T) {
	rec := runCancel(t, false, &cancelJobsRepo{}, &cancelAgent{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// The finalizer sealed the job between Get and the cancel: keep its outcome.
func TestCancel_LostTheRaceToTheFinalizer_Conflict(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: false}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"running"}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusConflict || errorCode(t, rec) != "job_already_finished" {
		t.Fatalf("code=%d error=%q, want 409 job_already_finished", rec.Code, errorCode(t, rec))
	}
}
