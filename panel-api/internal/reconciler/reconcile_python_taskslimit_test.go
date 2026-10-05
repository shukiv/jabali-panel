package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1820: app.python.apply returns a hint when the app is not active because
// the account's slice is at its Max tasks limit. The journal tail alone only
// shows the symptom (a worker that could not start).

const tasksLimitHintText = "the account has reached its Max tasks limit (21 of 20 in use), so no new process can start until one exits or an administrator raises the limit"

type tasksLimitAgent struct{}

func (tasksLimitAgent) Call(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"active": false,
		"unit":   "jabali-app-01APP.service",
		"detail": "BlockingIOError: [Errno 11] Resource temporarily unavailable",
		"hint":   tasksLimitHintText,
	})
}

type countingStatusRepo struct {
	statusCaptureRepo
	calls int
}

func (s *countingStatusRepo) UpdateStatus(ctx context.Context, id, status string, lastErr *string) error {
	s.calls++
	return s.statusCaptureRepo.UpdateStatus(ctx, id, status, lastErr)
}

func tasksLimitReconciler(repo *countingStatusRepo) *Reconciler {
	uname := "tenant"
	return &Reconciler{
		agent:      tasksLimitAgent{},
		pythonApps: repo,
		users:      notActiveUsers{u: &models.User{ID: "01USER", Username: &uname}},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func tasksLimitApp(status string, lastErr *string) *models.PythonApp {
	port := 8123
	return &models.PythonApp{
		ID: "01APP", UserID: "01USER", AppRoot: "/home/tenant/app",
		PythonVersion: "3.11", AppType: "wsgi", Entrypoint: "wsgi:app",
		Status: status, LastError: lastErr, LoopbackPort: &port,
	}
}

func TestReconcileOnePythonApp_NotActive_LeadsWithTasksLimit(t *testing.T) {
	repo := &countingStatusRepo{}
	tasksLimitReconciler(repo).reconcileOnePythonApp(context.Background(), tasksLimitApp(models.PythonAppStatusPending, nil))

	if !strings.HasPrefix(repo.lastErr, "The app is not running because "+tasksLimitHintText) {
		t.Fatalf("last_error must lead with the Max tasks limit; got %q", repo.lastErr)
	}
	if !strings.Contains(repo.lastErr, "BlockingIOError") {
		t.Fatalf("the journal tail must still follow; got %q", repo.lastErr)
	}
}

// An app already marked failed for an earlier reason must pick up the new one.
// Before GH #1820 the status write was skipped whenever the status itself did
// not change, so the row kept its first reason forever.
func TestReconcileOnePythonApp_FailedApp_RefreshesReason(t *testing.T) {
	old := "app started but is not active — check the app logs"
	repo := &countingStatusRepo{}
	tasksLimitReconciler(repo).reconcileOnePythonApp(context.Background(), tasksLimitApp(models.PythonAppStatusFailed, &old))

	if repo.calls != 1 || !strings.Contains(repo.lastErr, "Max tasks limit") {
		t.Fatalf("a changed reason must be written (calls=%d); got %q", repo.calls, repo.lastErr)
	}
}

// The same reason on the next tick is not rewritten.
func TestReconcileOnePythonApp_FailedApp_SameReasonNoWrite(t *testing.T) {
	first := &countingStatusRepo{}
	tasksLimitReconciler(first).reconcileOnePythonApp(context.Background(), tasksLimitApp(models.PythonAppStatusPending, nil))
	stored := first.lastErr

	repo := &countingStatusRepo{}
	tasksLimitReconciler(repo).reconcileOnePythonApp(context.Background(), tasksLimitApp(models.PythonAppStatusFailed, &stored))
	if repo.calls != 0 {
		t.Fatalf("an unchanged status and reason must not be rewritten; got %d writes", repo.calls)
	}
}
