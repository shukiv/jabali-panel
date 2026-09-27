package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func (f *fakeIMAPJobs) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
	return nil
}

type recordingMigAgent struct {
	mu    sync.Mutex
	calls []string
	jobs  []string
	err   error
}

func (a *recordingMigAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, cmd)
	if m, ok := params.(map[string]any); ok {
		if id, ok := m["job_id"].(string); ok {
			a.jobs = append(a.jobs, id)
		}
	}
	return json.RawMessage("{}"), a.err
}

func fireMigrationJob(fn func(*gin.Context), method, id string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(method, "/", nil)
	fn(c)
	return w
}

// JAB-357: the panel runs as the jabali user and cannot unlink in the
// root:jabali 0750 migration-secrets directory, so its own
// migrate.WipeJobSecret failed with EACCES on every REST cancel and destroy,
// leaving the source credentials on disk until the daily reaper. Both paths
// now ask the root Agent to remove the job's secret file.
func TestMigrationCancelAndDestroy_WipeTheSecretThroughTheAgent(t *testing.T) {
	const id = "01KZ0000000000000000000000"

	for _, tc := range []struct {
		name  string
		state string
		call  func(*adminMigrationsHandler) func(*gin.Context)
		verb  string
	}{
		{"cancel", models.MigrationStatePending, func(h *adminMigrationsHandler) func(*gin.Context) { return h.cancel }, http.MethodDelete},
		{"destroy", models.MigrationStateDone, func(h *adminMigrationsHandler) func(*gin.Context) { return h.destroy }, http.MethodDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, agentErr := range []error{nil, errors.New("agent down")} {
				ag := &recordingMigAgent{err: agentErr}
				h, jobs := newIMAPHandler(ag)
				jobs.byID[id] = &models.MigrationJob{ID: id, State: tc.state}

				w := fireMigrationJob(tc.call(h), tc.verb, id)
				if w.Code != http.StatusOK {
					t.Fatalf("agent err %v: code=%d, want 200 (%s)", agentErr, w.Code, w.Body.String())
				}
				if len(ag.calls) != 1 || ag.calls[0] != "migration.secrets_wipe" || len(ag.jobs) != 1 || ag.jobs[0] != id {
					t.Fatalf("agent err %v: calls=%v jobs=%v, want one migration.secrets_wipe for %s", agentErr, ag.calls, ag.jobs, id)
				}
			}
		})
	}
}
