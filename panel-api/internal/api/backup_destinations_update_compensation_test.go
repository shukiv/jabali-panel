package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// credsRecordingAgent records every agent command in order and returns a canned
// creds_write reply so writeCreds succeeds. It lets a test assert whether the
// destination's credential file was removed (backup.dest.creds_delete).
type credsRecordingAgent struct {
	commands []string
}

func (a *credsRecordingAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.commands = append(a.commands, cmd)
	if cmd == "backup.dest.creds_write" {
		return json.RawMessage(`{"path":"/etc/jabali-panel/restic-remotes/dst-1.env"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (a *credsRecordingAgent) count(cmd string) int {
	n := 0
	for _, c := range a.commands {
		if c == cmd {
			n++
		}
	}
	return n
}

// updFakeDestRepo returns a preset destination from Get and a preset error from
// Update. Every other repository method is promoted from the embedded nil
// interface and panics if called — the update handler calls only Get and Update.
type updFakeDestRepo struct {
	repository.BackupDestinationRepository
	getDest   *models.BackupDestination
	updateErr error
}

func (r *updFakeDestRepo) Get(_ context.Context, _ string) (*models.BackupDestination, error) {
	return r.getDest, nil
}

func (r *updFakeDestRepo) Update(_ context.Context, _ *models.BackupDestination) error {
	return r.updateErr
}

// updDo drives backupDestinationHandler.update directly (skipping the admin
// middleware) with a PATCH body and an :id param.
func updDo(h *backupDestinationHandler, id string, body map[string]any) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw, _ := json.Marshal(body)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/admin/backup-destinations/"+id, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.update(c)
	return w
}

// A first-time credential write (row had no file) followed by a failed persist
// must remove the just-written file, or it orphans a root:root 0600 secrets file
// behind a row whose credentials_ref stayed NULL. Load-bearing.
func TestBackupDestinationUpdate_CompensatesOrphanOnPersistFail(t *testing.T) {
	ag := &credsRecordingAgent{}
	repo := &updFakeDestRepo{
		getDest:   &models.BackupDestination{ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindS3},
		updateErr: errors.New("db down"),
	}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"credentials_env": map[string]string{"AWS_SECRET_ACCESS_KEY": "x"}})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if got := ag.count("backup.dest.creds_write"); got != 1 {
		t.Fatalf("creds_write fired %d times, want 1", got)
	}
	if got := ag.count("backup.dest.creds_delete"); got != 1 {
		t.Fatalf("orphaned credential file not compensated: creds_delete fired %d times, want 1", got)
	}
}

// A PRE-EXISTING credential file (the row already referenced one) must NOT be
// deleted on a failed persist: the surviving row still points at it (the path is
// deterministic per id), so deleting it would break the live destination.
// Load-bearing.
func TestBackupDestinationUpdate_PreExistingRefNotDeletedOnFail(t *testing.T) {
	ref := "/etc/jabali-panel/restic-remotes/dst-1.env"
	ag := &credsRecordingAgent{}
	repo := &updFakeDestRepo{
		getDest:   &models.BackupDestination{ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindS3, CredentialsRef: &ref},
		updateErr: errors.New("db down"),
	}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"credentials_env": map[string]string{"AWS_SECRET_ACCESS_KEY": "x"}})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if got := ag.count("backup.dest.creds_delete"); got != 0 {
		t.Fatalf("pre-existing credential file wrongly deleted on failure: creds_delete fired %d times, want 0", got)
	}
}

// A metadata-only update (no credentials_env) writes no file, so a failed
// persist must touch neither creds verb.
func TestBackupDestinationUpdate_NoCredsNoDelete(t *testing.T) {
	ag := &credsRecordingAgent{}
	repo := &updFakeDestRepo{
		getDest:   &models.BackupDestination{ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindS3},
		updateErr: errors.New("db down"),
	}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"enabled": false})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if wr, del := ag.count("backup.dest.creds_write"), ag.count("backup.dest.creds_delete"); wr != 0 || del != 0 {
		t.Fatalf("no credential file written this call, but agent creds verbs fired: write=%d delete=%d", wr, del)
	}
}

// A successful update must not remove the credential file it just wrote.
func TestBackupDestinationUpdate_SuccessNoCompensation(t *testing.T) {
	ag := &credsRecordingAgent{}
	repo := &updFakeDestRepo{
		getDest: &models.BackupDestination{ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindS3},
		// updateErr nil => persist succeeds
	}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"credentials_env": map[string]string{"AWS_SECRET_ACCESS_KEY": "x"}})

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if got := ag.count("backup.dest.creds_delete"); got != 0 {
		t.Fatalf("successful update must not remove the credential file: creds_delete fired %d times", got)
	}
}

// Source-pin: origHadCredsFile must be captured BEFORE the --clear-creds block.
// The behavioral matrix above cannot exercise a clear-then-rewrite in the same
// call cleanly (two creds_delete sources), so pin the ordering directly. If the
// capture moved after --clear-creds, a clear-then-rewrite would read
// origHadCredsFile as false and wrongly delete a file the surviving row still
// references.
func TestBackupDestinationUpdate_CapturesOrigRefBeforeClearCreds(t *testing.T) {
	src, err := os.ReadFile("backup_destinations.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	capIdx := strings.Index(body, "origHadCredsFile := d.CredentialsRef != nil")
	if capIdx < 0 {
		t.Fatal("origHadCredsFile capture not found in the update handler")
	}
	clearIdx := strings.Index(body, "if req.ClearCreds {")
	if clearIdx < 0 {
		t.Fatal("--clear-creds block not found")
	}
	if capIdx > clearIdx {
		t.Fatal("origHadCredsFile must be captured BEFORE the --clear-creds block")
	}
}
