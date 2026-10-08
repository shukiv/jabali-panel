package reconciler

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1956: the update check offers an install the image Update would move
// it to. An Odoo 19 install isn't told Odoo 20 is its update.

var (
	trackOdoo20 = "odoo:20.0@sha256:" + strings.Repeat("2", 64)
	trackOdoo19 = "odoo:19.0@sha256:" + strings.Repeat("1", 64)
)

func trackCatalog(t *testing.T) *dockerapp.Catalog {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "odoo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	app := `slug: odoo
name: Odoo
version: "20.0"
track: "20"
description: x
image_channel: ` + trackOdoo20 + `
held_tracks:
  - track: "19"
    version: "19.0"
    image_channel: ` + trackOdoo19 + `
volumes:
  - name: data
    container_path: /data
ports:
  - name: http
    container_port: 8069
    protocol: tcp
    default_enabled: true
    default_bind: loopback
    default_reverse_proxy: true
`
	os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(app), 0o644)
	os.WriteFile(filepath.Join(dir, "compose.yml.tmpl"), []byte("services:\n  odoo:\n    image: {{ .ImageChannel }}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg/>`), 0o644)
	cat, errs := dockerapp.LoadDir(filepath.Dir(dir))
	if len(errs) > 0 {
		t.Fatalf("catalog load: %v", errs)
	}
	return cat
}

type trackCheckRepo struct {
	gateDockerRepo
	checked int
}

func (f *trackCheckRepo) MarkChecked(context.Context, string) error {
	f.checked++
	return nil
}

func pollTrack(t *testing.T, version string) (*gateAgent, *trackCheckRepo) {
	t.Helper()
	ag := &gateAgent{}
	repo := &trackCheckRepo{}
	r := &Reconciler{agent: ag, dockerApps: repo, dockerCatalog: trackCatalog(t), log: slog.Default()}
	r.pollImageUpdate(context.Background(), &models.DockerApp{
		ID: "a1", Slug: "odoo", CatalogVersion: version,
		UpdateMode: models.DockerAppUpdateModeManual, Status: models.DockerAppStatusRunning,
	})
	return ag, repo
}

func TestPollImageUpdate_ChecksTheInstallsOwnTrack(t *testing.T) {
	for version, want := range map[string]string{"19.0": trackOdoo19, "20.0": trackOdoo20} {
		ag, repo := pollTrack(t, version)
		calls := ag.calls["docker_app.check_update"]
		if len(calls) != 1 || calls[0]["image_channel"] != want {
			t.Fatalf("%s: check_update calls %v, want image %s", version, calls, want)
		}
		if repo.checked != 1 {
			t.Fatalf("%s: marked checked %d times", version, repo.checked)
		}
	}
}

// An install with no update path isn't checked against an image it can't
// take, and is still marked checked so the poller moves on.
func TestPollImageUpdate_SkipsAnInstallWithoutAnUpdatePath(t *testing.T) {
	ag, repo := pollTrack(t, "17.0")
	if n := len(ag.calls["docker_app.check_update"]); n != 0 {
		t.Fatalf("check_update called %d times", n)
	}
	if repo.checked != 1 {
		t.Fatalf("marked checked %d times, want 1", repo.checked)
	}
}
