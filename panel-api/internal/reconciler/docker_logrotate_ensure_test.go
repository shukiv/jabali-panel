package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeLogrotateAgent struct{ ensureCalls []map[string]any }

func (f *fakeLogrotateAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "docker_app.logrotate_ensure" {
		if m, ok := params.(map[string]any); ok {
			f.ensureCalls = append(f.ensureCalls, m)
		}
	}
	return json.RawMessage(`{}`), nil
}

func loadReconcileTestCatalog(t *testing.T) *dockerapp.Catalog {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "install", "docker-apps")
	cat, errs := dockerapp.LoadDir(root)
	if len(errs) > 0 {
		t.Fatalf("catalog load: %v", errs)
	}
	return cat
}

// JAB-121 phase 2: an app whose catalog entry declares a log rotate volume gets
// a logrotate_ensure dispatch — exactly once per panel-api lifetime.
func TestEnsureLogrotate_DispatchesOncePerBoot(t *testing.T) {
	ag := &fakeLogrotateAgent{}
	r := &Reconciler{agent: ag, dockerCatalog: loadReconcileTestCatalog(t), log: slog.Default()}
	app := &models.DockerApp{ID: "app1", Slug: "flarum"}

	r.ensureLogrotate(context.Background(), app)
	if len(ag.ensureCalls) != 1 {
		t.Fatalf("want 1 ensure call, got %d", len(ag.ensureCalls))
	}
	specs, _ := ag.ensureCalls[0]["log_rotate"].([]map[string]any)
	if len(specs) == 0 || specs[0]["volume"] != "storagelogs" {
		t.Errorf("expected flarum storagelogs spec, got %v", ag.ensureCalls[0]["log_rotate"])
	}

	// second call → no re-dispatch (once per boot).
	r.ensureLogrotate(context.Background(), app)
	if len(ag.ensureCalls) != 1 {
		t.Errorf("second call re-dispatched; want 1 total, got %d", len(ag.ensureCalls))
	}
}

// An app whose catalog entry has NO rotate volume is a no-op (no agent call).
func TestEnsureLogrotate_NoLogVolumeSkipsAgent(t *testing.T) {
	ag := &fakeLogrotateAgent{}
	r := &Reconciler{agent: ag, dockerCatalog: loadReconcileTestCatalog(t), log: slog.Default()}
	app := &models.DockerApp{ID: "app2", Slug: "gitea"} // gitea has volumes but no rotate
	r.ensureLogrotate(context.Background(), app)
	if len(ag.ensureCalls) != 0 {
		t.Errorf("no-log app should not dispatch, got %d", len(ag.ensureCalls))
	}
}

// Unknown slug (not in catalog) is a safe no-op.
func TestEnsureLogrotate_UnknownSlugNoop(t *testing.T) {
	ag := &fakeLogrotateAgent{}
	r := &Reconciler{agent: ag, dockerCatalog: loadReconcileTestCatalog(t), log: slog.Default()}
	r.ensureLogrotate(context.Background(), &models.DockerApp{ID: "app3", Slug: "does-not-exist"})
	if len(ag.ensureCalls) != 0 {
		t.Errorf("unknown slug should not dispatch, got %d", len(ag.ensureCalls))
	}
}
