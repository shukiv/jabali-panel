package api

// A second submit of a manual backup, restore, full-backup package or PHP
// install while the first is still queued or running is refused with a 409,
// and nothing new is queued or sent to the agent.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type guardJobsRepo struct {
	repository.BackupJobRepository
	active  map[string]bool // "kind:user" → a queued or running job exists
	asked   []string
	created []*models.BackupJob
	run     []models.BackupJob
}

func (r *guardJobsRepo) HasActiveOfKind(_ context.Context, userID, kind string) (bool, error) {
	r.asked = append(r.asked, kind+":"+userID)
	return r.active[kind+":"+userID], nil
}

func (r *guardJobsRepo) Create(_ context.Context, j *models.BackupJob) error {
	r.created = append(r.created, j)
	return nil
}

func (r *guardJobsRepo) FindBySnapshotID(context.Context, string) (*models.BackupJob, error) {
	return nil, repository.ErrNotFound
}

func (r *guardJobsRepo) ListByRun(context.Context, string) ([]models.BackupJob, error) {
	return r.run, nil
}

type guardDests struct {
	repository.BackupDestinationRepository
}

func (guardDests) ListEnabled(context.Context) ([]models.BackupDestination, error) {
	return []models.BackupDestination{{ID: "dest1", Enabled: true, Kind: models.BackupDestinationKindLocal}}, nil
}

type guardUsers struct {
	repository.UserRepository
}

func (guardUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	name := "u" + strings.ToLower(id)
	return &models.User{ID: id, Username: &name}, nil
}

func guardCall(t *testing.T, handler func(*gin.Context), body string, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
	handler(c)
	return rec
}

func want409(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"`+code+`"`) {
		t.Fatalf("body = %s, want error %s", rec.Body, code)
	}
}

func TestRestore_RefusedWhileARestoreIsPending(t *testing.T) {
	jobs := &guardJobsRepo{active: map[string]bool{models.BackupJobKindAccountRestore + ":GUARDU1": true}}
	ag := &cancelAgent{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Agent: ag, Destinations: guardDests{}, Users: guardUsers{}}}

	rec := guardCall(t, h.restore, `{"manifest_snapshot_id":"snap1","target_user_id":"GUARDU1"}`, nil)

	want409(t, rec, "restore_in_progress")
	if len(jobs.created) != 0 || ag.calls != 0 {
		t.Fatalf("created %d jobs and made %d agent calls, want none", len(jobs.created), ag.calls)
	}
}

func TestCreateForUser_RefusedWhileABackupIsPending(t *testing.T) {
	jobs := &guardJobsRepo{active: map[string]bool{models.BackupJobKindAccountBackup + ":GUARDU2": true}}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Destinations: guardDests{}, Users: guardUsers{}}}

	rec := guardCall(t, h.createForUser, `{}`, gin.Params{{Key: "id", Value: "GUARDU2"}})

	want409(t, rec, "backup_in_progress")
	if len(jobs.created) != 0 {
		t.Fatalf("created %d jobs, want none", len(jobs.created))
	}
}

func TestCreateForUser_QueuesWhenNothingIsPending(t *testing.T) {
	jobs := &guardJobsRepo{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Destinations: guardDests{}, Users: guardUsers{}}}

	rec := guardCall(t, h.createForUser, `{}`, gin.Params{{Key: "id", Value: "GUARDU3"}})

	if rec.Code >= 400 {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
	}
	if len(jobs.created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(jobs.created))
	}
	if len(jobs.asked) != 1 || jobs.asked[0] != models.BackupJobKindAccountBackup+":GUARDU3" {
		t.Fatalf("asked %v, want the account's backups", jobs.asked)
	}
	// The slot is free again once the request is done.
	release, ok := backupSubmits.TryAcquire(models.BackupJobKindAccountBackup + ":GUARDU3")
	if !ok {
		t.Fatal("the submit slot was not released")
	}
	release()
}

// Two submits at once: the second finds the first's slot taken before the
// first has inserted its job.
func TestCreateForUser_RefusedWhileAnotherSubmitHoldsTheSlot(t *testing.T) {
	release, ok := backupSubmits.TryAcquire(models.BackupJobKindAccountBackup + ":GUARDU4")
	if !ok {
		t.Fatal("slot busy before the test")
	}
	defer release()
	jobs := &guardJobsRepo{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Destinations: guardDests{}, Users: guardUsers{}}}

	rec := guardCall(t, h.createForUser, `{}`, gin.Params{{Key: "id", Value: "GUARDU4"}})

	want409(t, rec, "backup_in_progress")
	if len(jobs.created) != 0 {
		t.Fatalf("created %d jobs, want none", len(jobs.created))
	}
}

func TestSystemCreate_RefusedWhileASystemBackupIsPending(t *testing.T) {
	jobs := &guardJobsRepo{active: map[string]bool{models.BackupJobKindSystemBackup + ":system": true}}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Destinations: guardDests{}}}

	rec := guardCall(t, h.systemCreate, `{}`, nil)

	want409(t, rec, "backup_in_progress")
	if len(jobs.created) != 0 {
		t.Fatalf("created %d jobs, want none", len(jobs.created))
	}
}

func TestFullBackupPackage_RefusedWhileTheRunIsBeingPackaged(t *testing.T) {
	restoreUploadDir = t.TempDir()
	t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
	const runID = "01HZGUARDPACK0000000000000"
	release, ok := fullBackupPacks.TryAcquire(runID)
	if !ok {
		t.Fatal("slot busy before the test")
	}
	defer release()
	jobs := &guardJobsRepo{run: []models.BackupJob{{ID: "j1", UserID: "system", Status: models.BackupJobStatusSucceeded, SnapshotID: "s1"}}}
	ag := &cancelAgent{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Agent: ag, Users: guardUsers{}}}

	rec := guardCall(t, h.fullBackupPackage, ``, gin.Params{{Key: "run_id", Value: runID}})

	want409(t, rec, "packaging_in_progress")
	if ag.calls != 0 {
		t.Fatalf("agent called %d times, want none", ag.calls)
	}
}

func TestPHPInstall_RefusedWhileTheVersionIsInstalling(t *testing.T) {
	release, ok := phpInstalls.TryAcquire("8.2")
	if !ok {
		t.Fatal("slot busy before the test")
	}
	defer release()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mock := agent.NewMockClient()
	RegisterPHPVersionAdminRoutes(r.Group("/api/v1/admin"), mock, nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/php/versions/8.2/install", nil))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "install_in_progress") {
		t.Fatalf("status = %d body %s, want 409 install_in_progress", rec.Code, rec.Body)
	}
	if n := len(mock.Calls()); n != 0 {
		t.Fatalf("agent called %d times, want none", n)
	}
}
