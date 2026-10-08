package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/uploadedbackups"
)

// GH #1993: an account backup an admin uploaded from another server stays on
// this one, listed in Backups, until the admin deletes it or its retention
// ends. A failed restore keeps it, so it can be restored again without
// uploading it again.

// memUploaded is an in-memory UploadedBackupRepository.
type memUploaded struct {
	mu   sync.Mutex
	rows map[string]*models.UploadedBackup
}

func newMemUploaded() *memUploaded { return &memUploaded{rows: map[string]*models.UploadedBackup{}} }

func (m *memUploaded) get(id string) *models.UploadedBackup {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.rows[id]; ok {
		cp := *b
		return &cp
	}
	return nil
}

func (m *memUploaded) Create(_ context.Context, b *models.UploadedBackup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *b
	m.rows[b.ID] = &cp
	return nil
}

func (m *memUploaded) FindByID(_ context.Context, id string) (*models.UploadedBackup, error) {
	if b := m.get(id); b != nil {
		return b, nil
	}
	return nil, repository.ErrNotFound
}

func (m *memUploaded) List(context.Context, repository.ListOptions) ([]models.UploadedBackup, int64, error) {
	rows, _ := m.ListAll(context.Background())
	return rows, int64(len(rows)), nil
}

func (m *memUploaded) ListAll(context.Context) ([]models.UploadedBackup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.UploadedBackup
	for _, b := range m.rows {
		out = append(out, *b)
	}
	return out, nil
}

func (m *memUploaded) ListExpired(context.Context, time.Time) ([]models.UploadedBackup, error) {
	return nil, nil
}

func (m *memUploaded) idle(b *models.UploadedBackup, staleBefore time.Time) bool {
	return b.RestoreStatus != models.UploadedBackupRestoring || b.RestoreStartedAt == nil || b.RestoreStartedAt.Before(staleBefore)
}

func (m *memUploaded) ClaimRestore(_ context.Context, id, target string, now, staleBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok {
		return repository.ErrNotFound
	}
	if !m.idle(b, staleBefore) {
		return repository.ErrUploadedBackupBusy
	}
	b.RestoreStatus, b.RestoreStartedAt, b.RestoreTarget, b.RestoreResult = models.UploadedBackupRestoring, &now, target, nil
	return nil
}

func (m *memUploaded) FinishRestore(_ context.Context, id, status, result string, now time.Time, expiresAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok {
		return repository.ErrNotFound
	}
	b.RestoreStatus, b.RestoredAt, b.RestoreResult = status, &now, &result
	if expiresAt != nil {
		b.ExpiresAt = expiresAt
	}
	return nil
}

func (m *memUploaded) DeleteIfIdle(_ context.Context, id string, staleBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok {
		return repository.ErrNotFound
	}
	if !m.idle(b, staleBefore) {
		return repository.ErrUploadedBackupBusy
	}
	delete(m.rows, id)
	return nil
}

// ubUsers knows the account alice.
type ubUsers struct{ ucUsers }

func (ubUsers) FindByUsername(_ context.Context, username string) (*models.User, error) {
	if username == "alice" {
		alice := "alice"
		return &models.User{ID: "T", Username: &alice}, nil
	}
	return nil, repository.ErrNotFound
}

const ubAdmin = "01KADMIN000000000000000000"

// ubAgent answers the agent calls a kept-backup flow makes. restore is the
// backup.restore_from_tar reply (an error fails it).
type ubAgent struct {
	inspectErr error
	// inspect is the backup.inspect_uploaded_tar reply; empty is alice's
	// archive, with nothing for the preflight to check.
	inspect string
	// phpVersions and phpExts answer php.version.list and php.ext.list
	// (version -> the ext list's extensions JSON).
	phpVersions string
	phpExts     map[string]string
	restore     func() (string, error)
	caps        string           // agent.version capabilities; empty = every one a restore needs
	seen        []map[string]any // backup.restore_from_tar params
}

func (a *ubAgent) agent() *mockAgent {
	return &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case "agent.version":
			caps := a.caps
			if caps == "" {
				caps = `"restore_upload_confinement","restore_keep_existing"`
			}
			return json.RawMessage(`{"version":"x","capabilities":[` + caps + `]}`), nil
		case "backup.inspect_uploaded_tar":
			if a.inspectErr != nil {
				return nil, a.inspectErr
			}
			if a.inspect != "" {
				return json.RawMessage(a.inspect), nil
			}
			return json.RawMessage(`{"user":{"username":"alice","email":"alice@example.org"},"components":["home","db","mail"],"preflight_supported":true}`), nil
		case "php.version.list":
			return json.RawMessage(`{"versions":[` + a.phpVersions + `]}`), nil
		case "php.ext.list":
			v := params.(map[string]string)["version"]
			ext, ok := a.phpExts[v]
			if !ok {
				return nil, fmt.Errorf("no ext list for %s", v)
			}
			return json.RawMessage(`{"version":"` + v + `","extensions":` + ext + `}`), nil
		case "backup.restore_from_tar":
			if p, ok := params.(map[string]any); ok {
				a.seen = append(a.seen, p)
			}
			if a.restore == nil {
				return json.RawMessage(`{"applied":["home → /home/alice"],"upload_confinement_enforced":true}`), nil
			}
			out, err := a.restore()
			return json.RawMessage(out), err
		}
		return nil, fmt.Errorf("unexpected %s", cmd)
	}}
}

type ubEnv struct {
	r    *gin.Engine
	repo *memUploaded
	// settings has every feature on; a test turns one off.
	settings *models.ServerSettings
}

// allFeaturesOn is server settings with PostgreSQL, mail, DNS and Docker apps
// for users on.
func allFeaturesOn() *models.ServerSettings {
	return &models.ServerSettings{PostgresEnabled: true, MailEnabled: true, DNSEnabled: true,
		DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: true}
}

func newUBEnv(t *testing.T, a *ubAgent) ubEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	restoreUploadDir = t.TempDir()
	uploadedbackups.Dir = t.TempDir()
	t.Cleanup(func() {
		restoreUploadDir = "/var/lib/jabali-uploads"
		uploadedbackups.Dir = uploadedbackups.DefaultDir
	})
	repo := newMemUploaded()
	cfg := ucConfig()
	cfg.Agent = a.agent()
	cfg.Users = ubUsers{}
	cfg.UploadedBackups = repo
	settings := allFeaturesOn()
	cfg.ServerSettings = &fakeSettingsRepo{s: settings}
	r := gin.New()
	v1 := r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: ubAdmin, IsAdmin: true})
		c.Next()
	})
	h := &backupHandler{cfg: cfg}
	h.registerUploadedBackupRoutes(v1.Group("/admin"))
	return ubEnv{r: r, repo: repo, settings: settings}
}

func (e ubEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func stage(t *testing.T, uploadID string) string {
	t.Helper()
	p := restoreUploadPath(ubAdmin, uploadID)
	if err := os.WriteFile(p, []byte("archive bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func keep(t *testing.T, e ubEnv, retention string) *models.UploadedBackup {
	t.Helper()
	b := &models.UploadedBackup{ID: "01KKEPT0000000000000000000", AccountUsername: "alice", Components: "home,db,mail", Retention: retention}
	_ = e.repo.Create(context.Background(), b)
	if err := os.WriteFile(uploadedbackups.Path(b.ID), []byte("archive bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	return b
}

func archivePresent(p string) bool { _, err := os.Stat(p); return err == nil }

func TestRegisterUploadedBackup_KeepsTheArchive(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	staged := stage(t, "upload-0001")

	w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups",
		map[string]any{"upload_id": "upload-0001", "retention": "keep_7_days", "file_name": "../alice\x00.tar.zst"})

	if w.Code != http.StatusCreated {
		t.Fatalf("status %d body %s, want 201", w.Code, w.Body)
	}
	var got struct {
		Data struct {
			ID              string     `json:"id"`
			AccountUsername string     `json:"account_username"`
			Components      []string   `json:"components"`
			Retention       string     `json:"retention"`
			ExpiresAt       *time.Time `json:"expires_at"`
			FileName        string     `json:"file_name"`
			TargetExists    *bool      `json:"target_exists"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	d := got.Data
	if d.AccountUsername != "alice" || strings.Join(d.Components, ",") != "home,db,mail" || d.Retention != "keep_7_days" {
		t.Errorf("row %+v, want alice / home,db,mail / keep_7_days", d)
	}
	if d.ExpiresAt == nil || time.Until(*d.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("expires_at %v, want about 7 days from now", d.ExpiresAt)
	}
	if d.FileName != "alice.tar.zst" {
		t.Errorf("file_name %q, want the base name without control characters", d.FileName)
	}
	if d.TargetExists == nil || !*d.TargetExists {
		t.Errorf("target_exists %v, want true (alice exists)", d.TargetExists)
	}
	if archivePresent(staged) || !archivePresent(uploadedbackups.Path(d.ID)) {
		t.Errorf("staged present=%v kept present=%v, want the archive moved to the kept dir", archivePresent(staged), archivePresent(uploadedbackups.Path(d.ID)))
	}
	if e.repo.get(d.ID) == nil {
		t.Error("no row was stored")
	}
}

func TestRegisterUploadedBackup_RefusesWhatIsNotAnAccountBackup(t *testing.T) {
	e := newUBEnv(t, &ubAgent{inspectErr: &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "archive has no manifest/manifest.json (not a Jabali account backup)"}})
	staged := stage(t, "upload-0002")

	w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups", map[string]any{"upload_id": "upload-0002"})

	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "not a Jabali account backup") {
		t.Fatalf("status %d body %s, want 422 with the agent's reason", w.Code, w.Body)
	}
	if rows, _ := e.repo.ListAll(context.Background()); len(rows) != 0 {
		t.Errorf("rows %v, want none", rows)
	}
	if !archivePresent(staged) {
		t.Error("the staged upload was removed; it should stay until the staging TTL")
	}
}

func TestRegisterUploadedBackup_ValidatesRetention(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	stage(t, "upload-0003")
	if w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups", map[string]any{"upload_id": "upload-0003", "retention": "forever"}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422 for an unknown retention", w.Code)
	}
}

// waitRestore waits for the detached restore of id to finish.
func waitRestore(t *testing.T, e ubEnv, id string) *models.UploadedBackup {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b := e.repo.get(id); b != nil && (b.RestoreStatus == models.UploadedBackupDone || b.RestoreStatus == models.UploadedBackupFailed) {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the restore did not finish")
	return nil
}

func TestRestoreUploadedBackup_KeepsTheArchive(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	b := keep(t, e, models.UploadedBackupKeep)

	w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore",
		map[string]any{"target_username": "alice", "components": []string{"home"}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s, want 202", w.Code, w.Body)
	}
	got := waitRestore(t, e, b.ID)
	if got == nil || got.RestoreStatus != models.UploadedBackupDone || got.RestoreTarget != "alice" {
		t.Fatalf("row %+v, want done into alice", got)
	}
	if got.RestoreResult == nil || !strings.Contains(*got.RestoreResult, "home → /home/alice") {
		t.Errorf("result %v, want the agent's applied items", got.RestoreResult)
	}
	if !archivePresent(uploadedbackups.Path(b.ID)) {
		t.Error("the archive was removed after the restore; it must stay")
	}
}

func TestRestoreUploadedBackup_DeleteAfterRestoreOnlyOnSuccess(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		e := newUBEnv(t, &ubAgent{})
		b := keep(t, e, models.UploadedBackupDeleteAfterRestore)
		e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"})
		got := waitRestore(t, e, b.ID)
		// The archive goes at once; the row stays a day so the report can be read.
		if archivePresent(uploadedbackups.Path(b.ID)) {
			t.Fatal("the archive is still there after a successful restore")
		}
		if got.ExpiresAt == nil || time.Until(*got.ExpiresAt) > 25*time.Hour || time.Until(*got.ExpiresAt) < 23*time.Hour {
			t.Errorf("expires_at %v, want the row removed about a day later", got.ExpiresAt)
		}
	})
	t.Run("failure", func(t *testing.T) {
		e := newUBEnv(t, &ubAgent{restore: func() (string, error) {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "disk full"}
		}})
		b := keep(t, e, models.UploadedBackupDeleteAfterRestore)
		e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"})
		got := waitRestore(t, e, b.ID)
		if got == nil || got.RestoreStatus != models.UploadedBackupFailed || !archivePresent(uploadedbackups.Path(b.ID)) {
			t.Fatalf("row %+v file present=%v, want failed and the archive kept for a retry", got, archivePresent(uploadedbackups.Path(b.ID)))
		}
		if got.RestoreResult == nil || !strings.Contains(*got.RestoreResult, "disk full") {
			t.Errorf("result %v, want the failure reason", got.RestoreResult)
		}
	})
}

func TestUploadedBackup_RefusedWhileRestoring(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	b := keep(t, e, models.UploadedBackupKeep)
	now := time.Now().UTC()
	_ = e.repo.ClaimRestore(context.Background(), b.ID, "alice", now, now.Add(-time.Hour))

	if w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"}); w.Code != http.StatusConflict {
		t.Errorf("restore while restoring: status %d, want 409", w.Code)
	}
	if w := e.do(t, http.MethodDelete, "/api/v1/admin/uploaded-backups/"+b.ID, nil); w.Code != http.StatusConflict {
		t.Errorf("delete while restoring: status %d, want 409", w.Code)
	}
	if !archivePresent(uploadedbackups.Path(b.ID)) {
		t.Error("the archive was removed while a restore holds it")
	}
}

func TestDeleteUploadedBackup(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	b := keep(t, e, models.UploadedBackupKeep)

	if w := e.do(t, http.MethodDelete, "/api/v1/admin/uploaded-backups/"+b.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("status %d body %s, want 200", w.Code, w.Body)
	}
	if e.repo.get(b.ID) != nil || archivePresent(uploadedbackups.Path(b.ID)) {
		t.Error("the row or the archive is still there")
	}
}

func TestUploadedBackup_RejectsAnIDThatIsNotAULID(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	for _, path := range []string{"/api/v1/admin/uploaded-backups/..%2F..%2Fetc", "/api/v1/admin/uploaded-backups/abc"} {
		if w := e.do(t, http.MethodGet, path, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, w.Code)
		}
	}
}

func TestListUploadedBackups(t *testing.T) {
	e := newUBEnv(t, &ubAgent{})
	keep(t, e, models.UploadedBackupKeep)

	w := e.do(t, http.MethodGet, "/api/v1/admin/uploaded-backups", nil)
	var got struct {
		Data []struct {
			ID          string `json:"id"`
			FilePresent bool   `json:"file_present"`
		} `json:"data"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if got.Total != 1 || len(got.Data) != 1 || !got.Data[0].FilePresent {
		t.Errorf("list %+v, want one row with its archive present", got)
	}
}
