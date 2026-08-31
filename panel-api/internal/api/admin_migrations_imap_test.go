package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/imap"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

type fakeIMAPJobs struct {
	repository.MigrationJobRepository
	mu       sync.Mutex
	byKey    map[string]*models.MigrationJob // "kind|host|user"
	byID     map[string]*models.MigrationJob
	created  int
	stateLog []string
}

func newFakeIMAPJobs() *fakeIMAPJobs {
	return &fakeIMAPJobs{byKey: map[string]*models.MigrationJob{}, byID: map[string]*models.MigrationJob{}}
}

func imapKey(kind, host, user string) string { return kind + "|" + host + "|" + user }

func (f *fakeIMAPJobs) FindBySource(_ context.Context, kind, host, user string) (*models.MigrationJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.byKey[imapKey(kind, host, user)]; ok {
		return j, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeIMAPJobs) Create(_ context.Context, j *models.MigrationJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	f.byKey[imapKey(j.SourceKind, j.SourceHost, j.SourceUser)] = j
	f.byID[j.ID] = j
	return nil
}

func (f *fakeIMAPJobs) UpdateState(_ context.Context, id, state string, _ *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateLog = append(f.stateLog, state)
	if j, ok := f.byID[id]; ok {
		j.State = state
	}
	return nil
}

func (f *fakeIMAPJobs) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.stateLog))
	copy(out, f.stateLog)
	return out
}

type fakeIMAPSettings struct {
	repository.ServerSettingsRepository
}

func (fakeIMAPSettings) Get(_ context.Context) (*models.ServerSettings, error) {
	return nil, errors.New("none") // → DefaultWorkingFolder
}

type fakeIMAPAgent struct{}

func (fakeIMAPAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

func newIMAPHandler(agentCli agent.AgentInterface) (*adminMigrationsHandler, *fakeIMAPJobs) {
	jobs := newFakeIMAPJobs()
	h := &adminMigrationsHandler{cfg: AdminMigrationsHandlerConfig{
		Jobs:     jobs,
		Settings: fakeIMAPSettings{},
		Agent:    agentCli,
	}}
	return h, jobs
}

func postJSON(h func(*gin.Context), body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	return w
}

// TestRegisterAdminMigrationRoutesNoPanic guards against a gin routing
// conflict from the new static /imap segment coexisting with the /:id
// param routes (a boot-time panic that would otherwise only surface in
// the Playwright job).
func TestRegisterAdminMigrationRoutesNoPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	RegisterAdminMigrationRoutes(e.Group("/api/v1"), AdminMigrationsHandlerConfig{
		Jobs: newFakeIMAPJobs(), Settings: fakeIMAPSettings{}, Agent: fakeIMAPAgent{},
	})
}

// --- probe ---

func TestProbeIMAP(t *testing.T) {
	orig := imapProbeFn
	defer func() { imapProbeFn = orig }()
	imapProbeFn = func(_ context.Context, _ imap.ProbeConfig) (*imap.ProbeResult, error) {
		return &imap.ProbeResult{Folders: []imap.Folder{
			{Name: "INBOX", Role: "inbox", Selectable: true},
			{Name: "[Gmail]/Sent Mail", Role: "sent", Selectable: true},
		}}, nil
	}
	h, _ := newIMAPHandler(fakeIMAPAgent{})
	w := postJSON(h.probeIMAP, `{"host":"imap.gmail.com","user":"a@b.com","password":"pw"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Total   int `json:"total"`
		Folders []imap.Folder
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
}

func TestProbeIMAPAuthFailed(t *testing.T) {
	orig := imapProbeFn
	defer func() { imapProbeFn = orig }()
	imapProbeFn = func(_ context.Context, _ imap.ProbeConfig) (*imap.ProbeResult, error) {
		return nil, imap.ErrAuth
	}
	h, _ := newIMAPHandler(fakeIMAPAgent{})
	w := postJSON(h.probeIMAP, `{"host":"h","user":"u","password":"bad"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), "ErrAuth") || strings.Contains(w.Body.String(), "imap:") {
		t.Errorf("response leaks internal error text: %s", w.Body.String())
	}
}

// GH #1429: a connect failure is classified into an actionable, SAFE hint and a
// stable code — and the raw error / remote address never reaches the client.
func TestProbeIMAPClassifiesConnectError(t *testing.T) {
	orig := imapProbeFn
	defer func() { imapProbeFn = orig }()
	imapProbeFn = func(_ context.Context, _ imap.ProbeConfig) (*imap.ProbeResult, error) {
		return nil, errors.New("imap: connect h:143: ssrf: 10.0.0.5 private range rejected")
	}
	h, _ := newIMAPHandler(fakeIMAPAgent{})
	w := postJSON(h.probeIMAP, `{"host":"h","user":"u","password":"pw"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "blocked_host") {
		t.Errorf("want error code blocked_host: %s", body)
	}
	if !strings.Contains(body, "Allow private/LAN host") {
		t.Errorf("want an actionable hint: %s", body)
	}
	if strings.Contains(body, "ssrf:") || strings.Contains(body, "10.0.0.5") {
		t.Errorf("response leaks raw error text: %s", body)
	}
}

func TestProbeIMAPValidation(t *testing.T) {
	h, _ := newIMAPHandler(fakeIMAPAgent{})
	w := postJSON(h.probeIMAP, `{"host":"h"}`) // missing user + password
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}

// --- run ---

func TestRunIMAPHappyPath(t *testing.T) {
	origImp := imapImportFn
	defer func() { imapImportFn = origImp }()
	done := make(chan imap.ImportConfig, 1)
	imapImportFn = func(_ context.Context, cfg imap.ImportConfig, _ agent.AgentInterface) (*imap.ImportResult, error) {
		done <- cfg
		return &imap.ImportResult{MessagesImported: 5, BytesImported: 500}, nil
	}

	h, jobs := newIMAPHandler(fakeIMAPAgent{})
	w := postJSON(h.runIMAP, `{"host":"imap.gmail.com","user":"a@b.com","password":"pw","to":"dest@local"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.JobID == "" || resp.State != models.MigrationStateRestoring {
		t.Fatalf("resp = %+v, want a job_id + restoring", resp)
	}

	select {
	case cfg := <-done:
		if cfg.TargetEmail != "dest@local" || cfg.JobID != resp.JobID {
			t.Errorf("import cfg = %+v", cfg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("import goroutine never ran")
	}

	// Terminal state lands as done (poll — goroutine writes after import).
	deadline := time.After(3 * time.Second)
	for {
		st := jobs.states()
		if len(st) > 0 && st[len(st)-1] == models.MigrationStateDone {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("state never reached done; log=%v", jobs.states())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if jobs.created != 1 {
		t.Errorf("created = %d, want 1", jobs.created)
	}
}

func TestRunIMAPAlreadyRunning(t *testing.T) {
	h, jobs := newIMAPHandler(fakeIMAPAgent{})
	jobs.byKey[imapKey(models.MigrationSourceIMAP, "h", "u")] = &models.MigrationJob{
		ID: "EXISTING", State: models.MigrationStateRestoring,
	}
	w := postJSON(h.runIMAP, `{"host":"h","user":"u","password":"pw","to":"d@l"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "EXISTING") {
		t.Errorf("409 body should carry existing job id: %s", w.Body.String())
	}
}

func TestRunIMAPAgentUnconfigured(t *testing.T) {
	h, _ := newIMAPHandler(nil) // Agent nil
	w := postJSON(h.runIMAP, `{"host":"h","user":"u","password":"pw","to":"d@l"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", w.Code)
	}
}

func TestRunIMAPValidation(t *testing.T) {
	h, _ := newIMAPHandler(fakeIMAPAgent{})
	w := postJSON(h.runIMAP, `{"host":"h","user":"u","password":"pw"}`) // missing to
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}
