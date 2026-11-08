package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A backup create or audit prune with a body that isn't valid JSON for the
// request is refused, instead of running with whatever fields decoded; an
// empty body still means "the defaults".

func wantInvalidJSON(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusBadRequest || !strings.Contains(body, `"invalid_json"`) {
		t.Fatalf("status = %d, body %s; want 400 invalid_json", code, body)
	}
}

func TestSystemCreate_RefusesABodyThatIsNotTheRequest(t *testing.T) {
	ag := &paramCaptureAgent{}
	jobs := &jobCaptureRepo{}
	h := systemBackupTestHandler(ag, jobs)
	// include_accounts as a string: decoding stops short of it.
	c, rec := systemCreateCtx(t, `{"destination_id":"d1","include_accounts":"yes"}`)
	h.systemCreate(c)

	wantInvalidJSON(t, rec.Code, rec.Body.String())
	if len(jobs.createdJobs()) != 0 || ag.called("system.backup") {
		t.Fatal("a refused request must create no job and reach no agent")
	}
}

func TestSystemCreate_AnEmptyBodyUsesTheDefaults(t *testing.T) {
	ag := &paramCaptureAgent{}
	jobs := &jobCaptureRepo{}
	h := systemBackupTestHandler(ag, jobs)
	h.cfg.Destinations = guardDests{} // no destination_id: the single enabled one is used
	c, rec := systemCreateCtx(t, "")
	h.systemCreate(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s; want 201", rec.Code, rec.Body)
	}
}

func TestCreateForUser_AnEmptyBodyUsesTheDefaults(t *testing.T) {
	jobs := &guardJobsRepo{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Destinations: guardDests{}, Users: guardUsers{}}}

	rec := guardCall(t, h.createForUser, "", gin.Params{{Key: "id", Value: "BODYU1"}})

	if rec.Code >= 400 || len(jobs.created) != 1 {
		t.Fatalf("status = %d, %d jobs; body %s; want one queued job", rec.Code, len(jobs.created), rec.Body)
	}
}

func TestCreateForUser_RefusesABodyThatIsNotTheRequest(t *testing.T) {
	jobs := &guardJobsRepo{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Destinations: guardDests{}, Users: guardUsers{}}}

	rec := guardCall(t, h.createForUser, `{"content":5}`, gin.Params{{Key: "id", Value: "BODYU2"}})

	wantInvalidJSON(t, rec.Code, rec.Body.String())
	if len(jobs.created) != 0 {
		t.Fatalf("created %d jobs, want none", len(jobs.created))
	}
}

func TestMeCreate_RefusesABodyThatIsNotTheRequest(t *testing.T) {
	jobs := &guardJobsRepo{}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Jobs: jobs, Users: guardUsers{admin: true}}}

	rec := guardCallAs(t, "BODYU3", h.create, `{"content":5}`, nil)

	wantInvalidJSON(t, rec.Code, rec.Body.String())
	if len(jobs.created) != 0 {
		t.Fatalf("created %d jobs, want none", len(jobs.created))
	}
}

// pruneAuditRepo records prunes and fails the audit row write on demand.
type pruneAuditRepo struct {
	repository.AuditEventRepository
	pruned    int
	createErr error
}

func (r *pruneAuditRepo) PruneOlderThanWithAnchor(context.Context, time.Time) (int64, error) {
	r.pruned++
	return 3, nil
}

func (r *pruneAuditRepo) Create(context.Context, *models.AuditEvent) error { return r.createErr }

func TestAuditPrune_RefusesABodyThatIsNotTheRequest(t *testing.T) {
	repo := &pruneAuditRepo{}
	h := &auditHandler{cfg: AuditHandlerConfig{Repo: repo}}

	rec := guardCall(t, h.prune, `{"days":"7"}`, nil)

	wantInvalidJSON(t, rec.Code, rec.Body.String())
	if repo.pruned != 0 {
		t.Fatal("a refused request must prune nothing")
	}
}

// The prune is recorded as an audit row (ADR-0106); when that write fails the
// failure is logged rather than dropped.
func TestAuditPrune_LogsAFailedRecordOfThePrune(t *testing.T) {
	repo := &pruneAuditRepo{createErr: errors.New("db gone")}
	var logs bytes.Buffer
	h := &auditHandler{cfg: AuditHandlerConfig{Repo: repo, Log: slog.New(slog.NewTextHandler(&logs, nil))}}

	rec := guardCall(t, h.prune, `{"days":30}`, nil)

	if rec.Code != http.StatusOK || repo.pruned != 1 {
		t.Fatalf("status = %d, pruned %d; want the prune to run", rec.Code, repo.pruned)
	}
	if !strings.Contains(logs.String(), "audit.retention.prune") || !strings.Contains(logs.String(), "db gone") {
		t.Fatalf("log = %q; want the failed prune record logged", logs.String())
	}
}
