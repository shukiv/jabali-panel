package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1956: Update never moves a Docker app across a major its app can't
// take in place, and an edit keeps it on its own release track.

func digestOf(c string) string { return "@sha256:" + strings.Repeat(c, 64) }

var (
	odoo20Image = "odoo:20.0" + digestOf("2")
	odoo19Image = "odoo:19.0" + digestOf("1")
	nc35Image   = "nextcloud:35-apache" + digestOf("5")
	nc34Image   = "nextcloud:34-apache" + digestOf("4")
)

// trackedCatalog writes two tracked entries shaped like the real odoo and
// nextcloud ones.
func trackedCatalog(t *testing.T) *dockerapp.Catalog {
	t.Helper()
	root := t.TempDir()
	tail := `description: x
volumes:
  - name: data
    container_path: /data
ports:
  - name: http
    container_port: 8080
    protocol: tcp
    default_enabled: true
    default_bind: loopback
    default_reverse_proxy: true
`
	apps := map[string]string{
		"odoo": `slug: odoo
name: Odoo
version: "20.0"
track: "20"
image_channel: ` + odoo20Image + `
held_tracks:
  - track: "19"
    version: "19.0"
    image_channel: ` + odoo19Image + `
` + tail,
		"nextcloud": `slug: nextcloud
name: Nextcloud
version: "35.0.1"
track: "35"
update_from: ["34"]
image_channel: ` + nc35Image + `
held_tracks:
  - track: "34"
    version: "34.0.3"
    image_channel: ` + nc34Image + `
    update_from: ["33"]
` + tail,
	}
	for slug, app := range apps {
		dir := filepath.Join(root, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(app), 0o644)
		os.WriteFile(filepath.Join(dir, "compose.yml.tmpl"), []byte("services:\n  app:\n    image: {{ .ImageChannel }}\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg/>`), 0o644)
	}
	cat, errs := dockerapp.LoadDir(root)
	if len(errs) > 0 {
		t.Fatalf("tracked catalog load errors: %v", errs)
	}
	return cat
}

// trackRepo records what a Docker app's row is told.
type trackRepo struct {
	repository.DockerAppRepository
	app *models.DockerApp

	mu        sync.Mutex
	statuses  []string
	versions  []string
	portDrops int
}

func (r *trackRepo) FindByID(context.Context, string) (*models.DockerApp, error) { return r.app, nil }
func (r *trackRepo) ListPortsForApp(context.Context, string) ([]*models.DockerAppPublishedPort, error) {
	return []*models.DockerAppPublishedPort{{ID: "p1", PortName: "http", HostPort: 10000, ContainerPort: 8080, BindInterface: "loopback", Protocol: "tcp"}}, nil
}
func (r *trackRepo) UpdateStatus(_ context.Context, _, status string, _ *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, status)
	return nil
}
func (r *trackRepo) UpdateCatalogVersion(_ context.Context, _, v string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions = append(r.versions, v)
	return nil
}
func (r *trackRepo) DeletePort(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.portDrops++
	return nil
}
func (r *trackRepo) UpdateImageSHA(context.Context, string, string) error        { return nil }
func (r *trackRepo) UpdateAvailableDigest(context.Context, string, string) error { return nil }

func (r *trackRepo) snapshot() (statuses, versions []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statuses...), append([]string(nil), r.versions...)
}

func trackHandler(t *testing.T, slug, version string) (*dockerAppHandler, *trackRepo, *agent.MockClient) {
	t.Helper()
	mock := agent.NewMockClient()
	mock.On("docker_app.read_env", map[string]any{"env": map[string]string{}})
	mock.On("docker_app.update", map[string]any{"outcome": "updated", "new_image": "sha256:new"})
	mock.On("docker_app.install", map[string]any{"status": "running"})
	repo := &trackRepo{app: &models.DockerApp{ID: "a1", Slug: slug, Name: slug, CatalogVersion: version, Status: models.DockerAppStatusStopped}}
	return &dockerAppHandler{cfg: DockerAppHandlerConfig{Catalog: trackedCatalog(t), Repo: repo, Agent: mock}}, repo, mock
}

func trackRequest(t *testing.T, fn gin.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/v1/admin/docker-apps/a1", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	fn(c)
	return w
}

// waitVersions waits for the background update to record a version label.
func waitVersions(t *testing.T, repo *trackRepo) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, v := repo.snapshot(); len(v) > 0 {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatal("the update recorded no version label")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func agentCalls(mock *agent.MockClient, command string) int {
	n := 0
	for _, c := range mock.Calls() {
		if c.Command == command {
			n++
		}
	}
	return n
}

// An install the catalog has no image for is refused before anything
// changes: no "updating" status, no agent call, no relabel. Without the
// guard the admin fallback "updated" it from its on-disk compose and
// labelled it with the catalog's version.
func TestUpdateImage_RefusedWithoutAnUpdatePath(t *testing.T) {
	h, repo, mock := trackHandler(t, "odoo", "17.0")
	w := trackRequest(t, h.updateImage, http.MethodPost, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d body %s, want 409", w.Code, w.Body)
	}
	var body struct{ Error, Detail string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "update_blocked" || !strings.Contains(body.Detail, "recorded as Odoo 17.0") {
		t.Fatalf("body %+v", body)
	}
	time.Sleep(50 * time.Millisecond)
	if statuses, versions := repo.snapshot(); len(statuses) != 0 || len(versions) != 0 {
		t.Fatalf("the row changed: statuses %v versions %v", statuses, versions)
	}
	if n := agentCalls(mock, "docker_app.update"); n != 0 {
		t.Fatalf("docker_app.update called %d times", n)
	}
}

func TestUpdateImage_FollowsTheReleaseTrack(t *testing.T) {
	for _, tc := range []struct {
		name, slug, recorded, image, label, notice string
	}{
		{"odoo 19 stays on 19 and is told why", "odoo", "19.0", odoo19Image, "19.0", "Odoo 20.0 is for new installs. This install stays on Odoo 19.0"},
		{"odoo 20 updates within 20", "odoo", "20.0", odoo20Image, "20.0", ""},
		{"nextcloud 34 moves to 35", "nextcloud", "34.0.2", nc35Image, "35.0.1", ""},
		{"nextcloud 33 steps to 34 first", "nextcloud", "33.0.5", nc34Image, "34.0.3", "Run Update again afterwards to reach 35.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, mock := trackHandler(t, tc.slug, tc.recorded)
			w := trackRequest(t, h.updateImage, http.MethodPost, "")
			if w.Code != http.StatusAccepted {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			var body struct{ Notice string }
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if (tc.notice == "") != (body.Notice == "") || !strings.Contains(body.Notice, tc.notice) {
				t.Fatalf("notice %q, want one containing %q", body.Notice, tc.notice)
			}
			if got := waitVersions(t, repo); len(got) != 1 || got[0] != tc.label {
				t.Fatalf("label %v, want %s", got, tc.label)
			}
			compose, _ := lastAgentParams(t, mock, "docker_app.update")["compose_yml"].(string)
			if !strings.Contains(compose, "image: "+tc.image+"\n") {
				t.Fatalf("compose %q, want image %s", compose, tc.image)
			}
		})
	}
}

// An env edit keeps the install on its own track, and its label follows the
// image the recreate put it on.
func TestApplyEnv_StaysOnTheInstallsTrack(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.2")
	if err := h.applyEnv(context.Background(), repo.app, map[string]string{}); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	compose, _ := lastAgentParams(t, mock, "docker_app.update")["compose_yml"].(string)
	if !strings.Contains(compose, "image: "+nc34Image+"\n") {
		t.Fatalf("compose %q, want the held 34 image", compose)
	}
	if _, versions := repo.snapshot(); len(versions) != 1 || versions[0] != "34.0.3" {
		t.Fatalf("label %v, want 34.0.3", versions)
	}
}

// An env edit on an install the catalog no longer has an image for is
// refused with what to do, before the container is touched.
func TestPutEnv_UpdateRequired(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "33.0.5")
	w := trackRequest(t, h.putEnv, http.MethodPut, `{"env":{"FOO":"bar"}}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d body %s, want 409", w.Code, w.Body)
	}
	var body struct{ Error, Detail string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "update_required" || !strings.Contains(body.Detail, "Update it first: Update moves it to 34.0.3.") {
		t.Fatalf("body %+v", body)
	}
	if statuses, _ := repo.snapshot(); len(statuses) != 0 || agentCalls(mock, "docker_app.update") != 0 {
		t.Fatalf("the container was touched: statuses %v calls %v", statuses, mock.Calls())
	}
}

// A domain or port edit checks for an image before it changes any row.
func TestEditDomainPorts_UpdateRequiredBeforeAnyChange(t *testing.T) {
	h, repo, mock := trackHandler(t, "odoo", "17.0")
	err := h.editDomainPorts(context.Background(), repo.app, nil, []installPortRequest{{Name: "http"}}, "admin", true)
	de, ok := err.(*dockerEditError)
	if !ok || de.Status != http.StatusConflict || de.Code != "update_required" {
		t.Fatalf("err %#v, want a 409 update_required", err)
	}
	if repo.portDrops != 0 || agentCalls(mock, "docker_app.install") != 0 {
		t.Fatalf("ports dropped %d, install calls %d", repo.portDrops, agentCalls(mock, "docker_app.install"))
	}
}

// The edit re-renders on the install's own track, and its label follows the
// image the edit put it on.
func TestEditDomainPorts_StaysOnTheInstallsTrack(t *testing.T) {
	for _, tc := range []struct {
		recorded string
		labels   []string
	}{
		{"34.0.2", []string{"34.0.3"}},
		{"34.0.3", nil},
	} {
		h, repo, mock := trackHandler(t, "nextcloud", tc.recorded)
		if err := h.editDomainPorts(context.Background(), repo.app, nil, nil, "admin", true); err != nil {
			t.Fatalf("%s: edit: %v", tc.recorded, err)
		}
		compose, _ := lastAgentParams(t, mock, "docker_app.install")["compose_yml"].(string)
		if !strings.Contains(compose, "image: "+nc34Image+"\n") {
			t.Fatalf("%s: compose %q, want the held 34 image", tc.recorded, compose)
		}
		if _, versions := repo.snapshot(); strings.Join(versions, ",") != strings.Join(tc.labels, ",") {
			t.Fatalf("%s: labels %v, want %v", tc.recorded, versions, tc.labels)
		}
	}
}
