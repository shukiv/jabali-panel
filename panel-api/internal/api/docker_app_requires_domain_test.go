package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// An app whose compose builds its address from the domain (Ghost, Vaultwarden,
// ...) crash-loops when installed without one, and an admin install allowed
// that. It is refused now, and so is an edit that removes the domain.

func requiresDomainCatalog(t *testing.T) *dockerapp.Catalog {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(`slug: notes
name: Notes
version: "1.0"
image_channel: notes:1.0`+digestOf("7")+`
description: x
requires_domain: true
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
`), 0o644)
	os.WriteFile(filepath.Join(dir, "compose.yml.tmpl"), []byte("services:\n  app:\n    image: {{ .ImageChannel }}\n    environment:\n      URL: \"https://{{ .Domain }}\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg/>`), 0o644)
	cat, errs := dockerapp.LoadDir(filepath.Dir(dir))
	if len(errs) > 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	return cat
}

// noDomainInstallRepo records the row an install creates.
type noDomainInstallRepo struct {
	*trackRepo
	created *models.DockerApp
}

func (r *noDomainInstallRepo) FindBySlugName(context.Context, string, string) (*models.DockerApp, error) {
	return nil, nil
}
func (r *noDomainInstallRepo) Create(_ context.Context, a *models.DockerApp) error {
	r.created = a
	return nil
}

func TestAdminDockerInstall_RefusesAnAppThatNeedsADomainWithoutOne(t *testing.T) {
	for _, body := range []string{
		`{"slug":"notes","name":"blog"}`,
		`{"slug":"notes","name":"blog","domain":"  "}`,
	} {
		repo := &noDomainInstallRepo{trackRepo: &trackRepo{}}
		mock := agent.NewMockClient()
		h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Catalog: requiresDomainCatalog(t), Repo: repo, Agent: mock}}

		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/docker-apps", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		h.install(c)

		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "domain_required") {
			t.Fatalf("%s: want 400 domain_required, got %d: %s", body, w.Code, w.Body.String())
		}
		if repo.created != nil || len(mock.Calls()) != 0 {
			t.Fatalf("%s: nothing may be created: row %+v, agent calls %d", body, repo.created, len(mock.Calls()))
		}
	}
}

func TestEditDomainPorts_RefusesRemovingTheDomainOfAnAppThatNeedsOne(t *testing.T) {
	mock := agent.NewMockClient()
	mock.On("docker_app.read_env", map[string]any{"env": map[string]string{}})
	mock.On("docker_app.install", map[string]any{"status": "running"})
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	tr := &trackRepo{app: &models.DockerApp{ID: "a1", Slug: "notes", Name: "notes", CatalogVersion: "1.0", Status: models.DockerAppStatusStopped}}
	dom := &editDomainRepo{listingDomainRepo: listingDomainRepo{&fakeDomainRepo{owned: []models.Domain{managedDomain("d1", "notes.example.com", "a1")}, byName: map[string]*models.Domain{}}}}
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Catalog: requiresDomainCatalog(t), Repo: movedPortRepo{tr}, Agent: mock, Domains: dom}}

	none := ""
	err := h.editDomainPorts(context.Background(), tr.app, &none, []installPortRequest{{Name: "http"}}, "u1", true)
	var ee *dockerEditError
	if !errors.As(err, &ee) || ee.Status != http.StatusBadRequest || ee.Code != "domain_required" {
		t.Fatalf("want 400 domain_required, got %v", err)
	}
	if len(dom.deleted) != 0 || tr.portDrops != 0 || len(mock.Calls()) != 0 {
		t.Fatalf("the app changed: domains deleted %v, ports dropped %d, agent calls %d", dom.deleted, tr.portDrops, len(mock.Calls()))
	}
}

func TestCatalogEntryToResponse_SaysWhenAnAppNeedsADomain(t *testing.T) {
	e, _ := requiresDomainCatalog(t).Get("notes")
	if !catalogEntryToResponse(e).RequiresDomain {
		t.Fatal("requires_domain missing from the catalog response")
	}
}
