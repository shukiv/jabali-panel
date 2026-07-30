package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #502: the system leg of a Full Server backup is commonly `partial` (a
// non-critical stage failed) yet its manifest snapshot is real and must be
// downloadable. streamBackupArtifact must pass a partial+snapshot job through
// the status gate — the SnapshotID check is the true gate — and only 404 the
// states that truly have no snapshot.
func newGateCtx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	return c, w
}

func noopLogErr(string, error, ...any) {}

func TestStreamBackupArtifact_AllowsPartial(t *testing.T) {
	c, w := newGateCtx()
	job := &models.BackupJob{Status: models.BackupJobStatusPartial, SnapshotID: "snap1"}
	// nil agent: a job that PASSES the status+snapshot gates reaches the
	// agent-nil check → 503. A 404 would mean the status gate rejected it.
	streamBackupArtifact(c, nil, job, nil, noopLogErr)
	if w.Code == http.StatusNotFound {
		t.Fatalf("partial+snapshot must not 404 at the status gate")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (gate passed, nil agent), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestStreamBackupArtifact_RejectsFailed(t *testing.T) {
	c, w := newGateCtx()
	job := &models.BackupJob{Status: models.BackupJobStatusFailed, SnapshotID: "snap1"}
	streamBackupArtifact(c, nil, job, nil, noopLogErr)
	if w.Code != http.StatusNotFound {
		t.Fatalf("failed job must 404, got %d", w.Code)
	}
}

func TestStreamBackupArtifact_PartialNeedsSnapshot(t *testing.T) {
	c, w := newGateCtx()
	job := &models.BackupJob{Status: models.BackupJobStatusPartial, SnapshotID: ""}
	streamBackupArtifact(c, nil, job, nil, noopLogErr)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("partial without a snapshot must 422, got %d", w.Code)
	}
}
