package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// delLogAgent records every agent command and can fail a chosen one, so a test
// can drive deleteCreds down its error branch.
type delLogAgent struct {
	calls   []string
	failCmd string
	failErr error
}

func (a *delLogAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	if a.failCmd != "" && cmd == a.failCmd {
		return nil, a.failErr
	}
	return json.RawMessage(`{}`), nil
}

func (a *delLogAgent) count(cmd string) int {
	n := 0
	for _, c := range a.calls {
		if c == cmd {
			n++
		}
	}
	return n
}

// delFakeDestRepo returns a preset destination from Get and a preset error from
// Delete. The delete handler calls only Get and Delete; every other method is
// promoted from the embedded nil interface and panics if called.
type delFakeDestRepo struct {
	repository.BackupDestinationRepository
	getDest   *models.BackupDestination
	deleteErr error
}

func (r *delFakeDestRepo) Get(_ context.Context, _ string) (*models.BackupDestination, error) {
	return r.getDest, nil
}

func (r *delFakeDestRepo) Delete(_ context.Context, _ string) error {
	return r.deleteErr
}

// delDo drives backupDestinationHandler.delete directly (skipping the admin
// middleware) with an :id param.
func delDo(h *backupDestinationHandler, id string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/backup-destinations/"+id, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.delete(c)
	return w
}

func delBufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))
}

func destWithCreds(id string) *models.BackupDestination {
	p := CredentialsDir + "/" + id + ".env"
	return &models.BackupDestination{ID: id, Name: "n", Kind: models.BackupDestinationKindS3, CredentialsRef: &p}
}

// A failed creds_delete no longer vanishes: the row is gone (200) but the
// leftover root:root 0600 credentials file is recorded at error level with its
// dest id and the underlying error, so an operator can reap it. Load-bearing —
// this is the whole point of the slice (visibility of the leak, not prevention).
func TestBackupDestinationDelete_LogsSwallowedCredsDeleteFailure(t *testing.T) {
	var buf bytes.Buffer
	ag := &delLogAgent{failCmd: "backup.dest.creds_delete", failErr: errors.New("agent boom")}
	h := &backupDestinationHandler{
		repo:  &delFakeDestRepo{getDest: destWithCreds("dst-1")},
		agent: ag,
		log:   delBufLogger(&buf),
	}

	w := delDo(h, "dst-1")

	if w.Code != http.StatusOK {
		t.Fatalf("delete code = %d, want 200 (a cleanup failure must stay non-fatal to the delete)", w.Code)
	}
	if ag.count("backup.dest.creds_delete") != 1 {
		t.Fatalf("creds_delete calls = %d, want 1", ag.count("backup.dest.creds_delete"))
	}
	logged := buf.String()
	if !strings.Contains(logged, "creds_delete") || !strings.Contains(logged, "dst-1") || !strings.Contains(logged, "agent boom") {
		t.Fatalf("failed creds_delete must be logged with dest id + error, got: %q", logged)
	}
}

// A creds_delete that succeeds emits no error log — we only shout when a
// secrets file is actually left behind.
func TestBackupDestinationDelete_NoLogWhenCredsDeleteSucceeds(t *testing.T) {
	var buf bytes.Buffer
	ag := &delLogAgent{}
	h := &backupDestinationHandler{
		repo:  &delFakeDestRepo{getDest: destWithCreds("dst-1")},
		agent: ag,
		log:   delBufLogger(&buf),
	}

	w := delDo(h, "dst-1")

	if w.Code != http.StatusOK {
		t.Fatalf("delete code = %d, want 200", w.Code)
	}
	if ag.count("backup.dest.creds_delete") != 1 {
		t.Fatalf("creds_delete calls = %d, want 1", ag.count("backup.dest.creds_delete"))
	}
	if buf.Len() != 0 {
		t.Fatalf("no error log expected on a successful cleanup, got: %q", buf.String())
	}
}

// A destination with no credential file triggers no creds_delete and no log.
func TestBackupDestinationDelete_NoCredsRefNoCallNoLog(t *testing.T) {
	var buf bytes.Buffer
	ag := &delLogAgent{failCmd: "backup.dest.creds_delete", failErr: errors.New("agent boom")}
	h := &backupDestinationHandler{
		repo:  &delFakeDestRepo{getDest: &models.BackupDestination{ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindS3}},
		agent: ag,
		log:   delBufLogger(&buf),
	}

	w := delDo(h, "dst-1")

	if w.Code != http.StatusOK {
		t.Fatalf("delete code = %d, want 200", w.Code)
	}
	if ag.count("backup.dest.creds_delete") != 0 {
		t.Fatalf("creds_delete must not fire when the row has no credential file: calls = %d", ag.count("backup.dest.creds_delete"))
	}
	if buf.Len() != 0 {
		t.Fatalf("no error log expected when nothing was cleaned up, got: %q", buf.String())
	}
}

// A nil agent keeps deleteCreds a no-op: no panic, no log (pins the existing
// early return).
func TestBackupDestinationDelete_NilAgentNoPanicNoLog(t *testing.T) {
	var buf bytes.Buffer
	h := &backupDestinationHandler{
		repo:  &delFakeDestRepo{getDest: destWithCreds("dst-1")},
		agent: nil,
		log:   delBufLogger(&buf),
	}

	w := delDo(h, "dst-1")

	if w.Code != http.StatusOK {
		t.Fatalf("delete code = %d, want 200", w.Code)
	}
	if buf.Len() != 0 {
		t.Fatalf("no error log expected with a nil agent, got: %q", buf.String())
	}
}
