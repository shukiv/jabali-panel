package api

import (
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
