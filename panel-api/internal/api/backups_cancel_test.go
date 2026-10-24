package api

// GH #2245: Cancel on the admin Backups page answered "Cancellation
// requested" but stopped nothing: backup.cancel was a no-op on the agent and
// the handler never touched the row, so the backup ran on and stayed
// "running". The handler now seals the row cancelled once nothing runs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
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
	reply   string
	err     error
	rpc     string
	calls   int
	timeout time.Duration // how long the handler let the call run
}

func (a *cancelAgent) Call(ctx context.Context, method string, _ any) (json.RawMessage, error) {
	a.calls++
	a.rpc = method
	if dl, ok := ctx.Deadline(); ok {
		a.timeout = time.Until(dl)
	}
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
	ag := &cancelAgent{reply: `{"job_id":"` + cancelTestJobID + `","stopped":true,"job_state":"cancelled"}`}

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
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"cancelled"}`}

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

	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "agent_cannot_cancel" || jobs.sealed != 0 {
		t.Fatalf("code=%d error=%q sealed=%d, want 500 agent_cannot_cancel, not sealed", rec.Code, errorCode(t, rec), jobs.sealed)
	}
}

func TestCancel_RunningJobAgentUnreachable_DoesNotSeal(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{err: errors.New("dial unix /run/jabali/agent.sock: connect: no such file or directory")}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "agent_call_failed" || jobs.sealed != 0 {
		t.Fatalf("code=%d error=%q sealed=%d, want 500 agent_call_failed, not sealed", rec.Code, errorCode(t, rec), jobs.sealed)
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
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"cancelled"}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusConflict || errorCode(t, rec) != "job_already_finished" {
		t.Fatalf("code=%d error=%q, want 409 job_already_finished", rec.Code, errorCode(t, rec))
	}
}

// Contract with the agent: decode the fixture the agent's own test pins its
// backup.cancel reply to (panel-agent commands/backup_job_registry_test.go).
func TestAgentCancelReply_MatchesTheAgentFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "panel-agent", "internal", "commands", "testdata", "backup_cancel_reply.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var reply agentCancelReply
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("agent backup.cancel reply does not decode into agentCancelReply: %v", err)
	}
	if reply.Stopped == nil || !*reply.Stopped || reply.JobState != "cancelled" {
		t.Fatalf("decoded %+v, want stopped=true job_state=cancelled", reply)
	}
}

// GH #2245 follow-up: the row said Cancelled while the agent was still
// stopping the backup, and a delete right after was refused because the job
// still held its slot. The agent now answers once the job has stopped; one
// that is still stopping is reported running, and the row must stay as it is
// until the finalizer reads "cancelled" from the agent.
func TestCancel_StillStopping_AnswersCancellingWithoutSealing(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"running"}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202 (%s)", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Data.Status != "cancelling" {
		t.Errorf("data.status = %q, want cancelling", body.Data.Status)
	}
	if jobs.sealed != 0 {
		t.Fatal("a backup that is still stopping was sealed cancelled")
	}
}

// It finished on its own while the agent waited for it to stop: keep the
// real outcome.
func TestCancel_FinishedWhileStopping_KeepsTheOutcome(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"succeeded"}`}

	rec := runCancel(t, false, jobs, ag)

	if rec.Code != http.StatusConflict || errorCode(t, rec) != "job_already_finished" || jobs.sealed != 0 {
		t.Fatalf("code=%d error=%q sealed=%d, want 409 job_already_finished, not sealed", rec.Code, errorCode(t, rec), jobs.sealed)
	}
}

// The agent waits up to restic's CancelGrace for the job to stop, so the call
// must outlast that, and still answer inside the server's 30s WriteTimeout.
func TestCancel_AgentCallOutlastsTheStopButNotTheWriteTimeout(t *testing.T) {
	jobs := &cancelJobsRepo{job: cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup), sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"cancelled"}`}

	runCancel(t, false, jobs, ag)

	if ag.timeout <= internalbackup.CancelGrace || ag.timeout >= 30*time.Second {
		t.Fatalf("agent call timeout = %s, want above CancelGrace (%s) and below 30s", ag.timeout, internalbackup.CancelGrace)
	}
}

func runMeCancel(t *testing.T, userID string, jobs *cancelJobsRepo, ag *cancelAgent) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/me/backups/"+cancelTestJobID+"/cancel", nil)
	c.Params = gin.Params{{Key: "id", Value: cancelTestJobID}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID})
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Jobs: jobs, Agent: ag}}
	h.cancel(c)
	return rec
}

// GH #2245: a tenant can cancel a queued or running backup of their own
// account, including one from an admin schedule (the same rows their Delete
// already reaches).
func TestMeCancel_OwnRunningBackup_SealsCancelled(t *testing.T) {
	job := cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup)
	job.UserID = "01HZ2245TENANT000000000001"
	jobs := &cancelJobsRepo{job: job, sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"cancelled"}`}

	rec := runMeCancel(t, job.UserID, jobs, ag)

	if rec.Code != http.StatusOK || ag.rpc != "backup.cancel" {
		t.Fatalf("code=%d rpc=%q, want 200 via backup.cancel (%s)", rec.Code, ag.rpc, rec.Body)
	}
	if jobs.sealed != 1 || jobs.sealText != cancelledByOwnerText {
		t.Fatalf("sealed %d times with %q, want once with %q", jobs.sealed, jobs.sealText, cancelledByOwnerText)
	}
}

func TestMeCancel_SomeoneElsesBackup_NotFound(t *testing.T) {
	job := cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindAccountBackup)
	job.UserID = "01HZ2245TENANT000000000001"
	jobs := &cancelJobsRepo{job: job, sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"cancelled"}`}

	rec := runMeCancel(t, "01HZ2245TENANT000000000002", jobs, ag)

	if rec.Code != http.StatusNotFound || ag.calls != 0 || jobs.sealed != 0 {
		t.Fatalf("code=%d agent calls=%d sealed=%d, want 404 and nothing touched", rec.Code, ag.calls, jobs.sealed)
	}
}

// A system backup is the server's, not the tenant's, even if its row names
// them.
func TestMeCancel_SystemBackup_NotFound(t *testing.T) {
	job := cancelTestJob(models.BackupJobStatusRunning, models.BackupJobKindSystemBackup)
	job.UserID = "01HZ2245TENANT000000000001"
	jobs := &cancelJobsRepo{job: job, sealResult: true}
	ag := &cancelAgent{reply: `{"stopped":true,"job_state":"cancelled"}`}

	rec := runMeCancel(t, job.UserID, jobs, ag)

	if rec.Code != http.StatusNotFound || ag.calls != 0 {
		t.Fatalf("code=%d agent calls=%d, want 404 and no agent call", rec.Code, ag.calls)
	}
}
