package reconciler

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// An auto-update stops the old containers before the new ones start only for
// an app whose catalog entry asks for it (stop_before_recreate).
func TestAutoUpdate_StopFirstFollowsTheCatalog(t *testing.T) {
	for slug, want := range map[string]bool{"rocketchat": true, "memos": false} {
		r, ag, _ := gateReconciler(t, nil)
		r.pollImageUpdate(context.Background(), &models.DockerApp{
			ID: "a1", Slug: slug,
			UpdateMode: models.DockerAppUpdateModeAuto, Status: models.DockerAppStatusRunning,
		})
		calls := ag.calls["docker_app.update"]
		if len(calls) != 1 {
			t.Fatalf("%s: want one auto-update, got %d", slug, len(calls))
		}
		if got := calls[0]["stop_first"] == true; got != want {
			t.Errorf("%s: stop_first = %v, want %v", slug, calls[0]["stop_first"], want)
		}
	}
}
