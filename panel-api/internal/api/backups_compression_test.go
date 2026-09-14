package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1646 (Slice 1): a System backup must honour the operator's restic
// compression choice. The level is validated against the off/auto/max whitelist
// and forwarded to the agent's system.backup. (The Full Server per-account
// fan-out that carries the level onto each account job is Slice 2.)

// paramCaptureAgent records the params of the last call for each command.
type paramCaptureAgent struct {
	mu     sync.Mutex
	params map[string]map[string]any
	calls  []string
}

func (a *paramCaptureAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.params == nil {
		a.params = map[string]map[string]any{}
	}
	a.calls = append(a.calls, command)
	if m, ok := params.(map[string]any); ok {
		a.params[command] = m
	}
	return json.RawMessage(`{}`), nil
}

func (a *paramCaptureAgent) paramsFor(command string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.params[command]
}

func (a *paramCaptureAgent) called(command string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c == command {
			return true
		}
	}
	return false
}

// jobCaptureRepo records every created job so fan-out and system-row
// compression can be asserted.
type jobCaptureRepo struct {
	repository.BackupJobRepository
	mu      sync.Mutex
	created []models.BackupJob
}

func (r *jobCaptureRepo) Create(_ context.Context, j *models.BackupJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, *j)
	return nil
}
func (r *jobCaptureRepo) MarkStarted(context.Context, string) error { return nil }
func (r *jobCaptureRepo) MarkFinished(context.Context, string, string, string, string, uint64, uint64, json.RawMessage, json.RawMessage, string) error {
	return nil
}

func (r *jobCaptureRepo) createdJobs() []models.BackupJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]models.BackupJob, len(r.created))
	copy(out, r.created)
	return out
}

// fanoutUsers returns a fixed set of non-admin users for the Full Server
// fan-out; List is the only method systemCreate reaches.
type fanoutUsers struct {
	repository.UserRepository
	users []models.User
}

func (u fanoutUsers) List(_ context.Context, _ repository.ListOptions) ([]models.User, int64, error) {
	return u.users, int64(len(u.users)), nil
}

func systemBackupTestHandler(ag *paramCaptureAgent, jobs *jobCaptureRepo) *backupHandler {
	un1, un2 := "alice", "bob"
	return &backupHandler{cfg: BackupHandlerConfig{
		Users:        fanoutUsers{users: []models.User{{ID: "u1", Username: &un1}, {ID: "u2", Username: &un2}}},
		Destinations: stubBackupDests{d: &models.BackupDestination{ID: "d1", Enabled: true, Kind: "local"}},
		Jobs:         jobs,
		Agent:        ag,
	}}
}

func systemCreateCtx(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/backups", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, rec
}

func TestSystemCreate_CompressionReachesAgent(t *testing.T) {
	ag := &paramCaptureAgent{}
	jobs := &jobCaptureRepo{}
	h := systemBackupTestHandler(ag, jobs)
	c, rec := systemCreateCtx(t, `{"destination_id":"d1","compression":"max"}`)
	h.systemCreate(c)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	p := ag.paramsFor("system.backup")
	require.NotNil(t, p, "system.backup must be dispatched")
	require.Equal(t, "max", p["compression"], "the chosen compression level must reach the agent")
}

func TestSystemCreate_InvalidCompressionRejected(t *testing.T) {
	ag := &paramCaptureAgent{}
	jobs := &jobCaptureRepo{}
	h := systemBackupTestHandler(ag, jobs)
	c, rec := systemCreateCtx(t, `{"destination_id":"d1","compression":"gzip"}`)
	h.systemCreate(c)

	require.Equal(t, http.StatusBadRequest, rec.Code, "an out-of-whitelist compression must be rejected")
	require.Empty(t, jobs.createdJobs(), "no job may be created for an invalid request")
	require.False(t, ag.called("system.backup"), "an invalid request must not reach the agent")
}
