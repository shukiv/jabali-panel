package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The status a lifecycle verb leaves an app in. The admin lifecycle door and
// `jabali docker-app <verb>` both write it, so the edit gate (status running
// means the container is up) holds right after a stop or start from either.
func TestDockerAppLifecycleStatus(t *testing.T) {
	for verb, want := range map[string]string{
		"start":   models.DockerAppStatusRunning,
		"restart": models.DockerAppStatusRunning,
		"rebuild": models.DockerAppStatusRunning,
		"stop":    models.DockerAppStatusStopped,
		"logs":    "",
		"":        "",
	} {
		if got := DockerAppLifecycleStatus(verb); got != want {
			t.Errorf("DockerAppLifecycleStatus(%q) = %q, want %q", verb, got, want)
		}
	}
}

// errRecordingRepo records the last_error an UpdateStatus writes.
type errRecordingRepo struct {
	*trackRepo
	status  string
	lastErr *string
	writes  int
}

func (r *errRecordingRepo) UpdateStatus(_ context.Context, _, status string, lastError *string) error {
	r.writes++
	r.status, r.lastErr = status, lastError
	return nil
}

// An edit redispatches the stack (compose up), so the container runs after it.
// The row used to keep its status: a failed install fixed by an edit (a free
// port) stayed failed for good, error and all, since the reconciler doesn't
// poll failed rows. It now goes to installing, which the reconciler's status
// poll moves on to running once the stack is healthy, and the stale error goes.
func TestEditDomainPorts_HandsTheRowToTheStatusPoll(t *testing.T) {
	h, repo, _ := trackHandler(t, "nextcloud", "34.0.3")
	msg := "agent: internal: docker compose up -d failed: exit status 1"
	repo.app.Status, repo.app.LastError = models.DockerAppStatusFailed, &msg
	rec := &errRecordingRepo{trackRepo: repo}
	h.cfg.Repo = rec
	if err := h.editDomainPorts(context.Background(), repo.app, nil, nil, "admin", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if rec.writes != 1 || rec.status != models.DockerAppStatusInstalling {
		t.Fatalf("edit wrote %d statuses, last %q; want one %q", rec.writes, rec.status, models.DockerAppStatusInstalling)
	}
	if rec.lastErr == nil || *rec.lastErr != "" {
		t.Fatalf("edit left last_error %v; want it cleared", rec.lastErr)
	}
}

// A redispatch that failed leaves the row as it was: the stack didn't come up.
func TestEditDomainPorts_FailedRedispatchKeepsTheStatus(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.3")
	mock.OnError("docker_app.install", errors.New("compose up failed"))
	rec := &errRecordingRepo{trackRepo: repo}
	h.cfg.Repo = rec
	if err := h.editDomainPorts(context.Background(), repo.app, nil, nil, "admin", true); err == nil {
		t.Fatal("want the redispatch error")
	}
	if rec.writes != 0 {
		t.Fatalf("a failed redispatch wrote status %q", rec.status)
	}
}

func TestAdminLifecycle_RecordsTheVerbsStatus(t *testing.T) {
	for verb, want := range map[string]string{
		"stop":    models.DockerAppStatusStopped,
		"start":   models.DockerAppStatusRunning,
		"rebuild": models.DockerAppStatusRunning,
	} {
		t.Run(verb, func(t *testing.T) {
			h, repo, mock := trackHandler(t, "privatebin", "1.0")
			mock.On("docker_app."+verb, map[string]any{"ok": true})
			w := trackRequest(t, h.lifecycle(verb), http.MethodPost, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			statuses, _ := repo.snapshot()
			if len(statuses) != 1 || statuses[0] != want {
				t.Fatalf("%s wrote statuses %v, want [%s]", verb, statuses, want)
			}
		})
	}
}
