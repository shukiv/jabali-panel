package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #357: app.python.apply now answers {"building": true} while the agent
// installs the venv and requirements in the background. That is progress, not
// a failure.

type buildingAgent struct{}

func (buildingAgent) Call(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"active": false, "unit": "jabali-app-01APP.service", "building": true})
}

func buildingReconciler(repo *countingStatusRepo) *Reconciler {
	uname := "tenant"
	return &Reconciler{
		agent:      buildingAgent{},
		pythonApps: repo,
		users:      notActiveUsers{u: &models.User{ID: "01USER", Username: &uname}},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestReconcileOnePythonApp_BuildingIsNotFailed(t *testing.T) {
	old := "agent: read: read unix @->/run/jabali/agent.sock: i/o timeout"
	repo := &countingStatusRepo{}
	buildingReconciler(repo).reconcileOnePythonApp(context.Background(), tasksLimitApp(models.PythonAppStatusFailed, &old))

	if repo.status != models.PythonAppStatusBuilding {
		t.Fatalf("status = %q, want building", repo.status)
	}
	if repo.lastErr != "" {
		t.Fatalf("a running build must clear the old error; got %q", repo.lastErr)
	}
}

func TestReconcileOnePythonApp_StillBuildingNoRewrite(t *testing.T) {
	repo := &countingStatusRepo{}
	buildingReconciler(repo).reconcileOnePythonApp(context.Background(), tasksLimitApp(models.PythonAppStatusBuilding, nil))
	if repo.calls != 0 {
		t.Fatalf("an app already shown as building must not be rewritten each tick; got %d writes", repo.calls)
	}
}
