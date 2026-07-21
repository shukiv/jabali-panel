package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// notActiveAgent mimics app.python.apply returning "started but not active" with
// the unit journal tail in detail (GH #357).
type notActiveAgent struct{}

func (notActiveAgent) Call(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"active": false,
		"unit":   "jabali-app-01APP.service",
		"detail": "ModuleNotFoundError: No module named 'wsgi'\ngunicorn worker failed to boot",
	})
}

type notActiveUsers struct {
	repository.UserRepository
	u *models.User
}

func (s notActiveUsers) FindByID(context.Context, string) (*models.User, error) { return s.u, nil }

type statusCaptureRepo struct {
	repository.PythonAppRepository
	status  string
	lastErr string
}

func (s *statusCaptureRepo) ListEnv(context.Context, string) ([]*models.PythonAppEnv, error) {
	return nil, nil
}

func (s *statusCaptureRepo) UpdateStatus(_ context.Context, _, status string, lastErr *string) error {
	s.status = status
	if lastErr != nil {
		s.lastErr = *lastErr
	}
	return nil
}

// GH #357: when a python app starts but isn't active, the reconciler must put
// the agent-captured journal tail into last_error. The reporter's "nothing in
// logs" wall was a generic "not active" with no reason — this asserts the real
// startup error now reaches the app record (and thus the panel).
func TestReconcileOnePythonApp_NotActive_SurfacesJournal(t *testing.T) {
	uname := "tenant"
	pyRepo := &statusCaptureRepo{}
	port := 8123
	r := &Reconciler{
		agent:      notActiveAgent{},
		pythonApps: pyRepo,
		users:      notActiveUsers{u: &models.User{ID: "01USER", Username: &uname}},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app := &models.PythonApp{
		ID: "01APP", UserID: "01USER", AppRoot: "/home/tenant/app",
		PythonVersion: "3.12", AppType: "wsgi", Entrypoint: "wsgi:app",
		Status: models.PythonAppStatusPending, LoopbackPort: &port,
	}

	r.reconcileOnePythonApp(context.Background(), app)

	if pyRepo.status != models.PythonAppStatusFailed {
		t.Fatalf("status = %q, want failed", pyRepo.status)
	}
	if !strings.Contains(pyRepo.lastErr, "ModuleNotFoundError") {
		t.Errorf("last_error must carry the agent journal detail; got %q", pyRepo.lastErr)
	}
	if !strings.Contains(pyRepo.lastErr, "jabali-app-01APP.service") {
		t.Errorf("last_error should reference the unit; got %q", pyRepo.lastErr)
	}
}
