package api

import (
	"context"
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

// A ports-only edit (`jabali docker-app edit --port` without --domain, or a
// PATCH with ports only) skipped the domain: the app's domain row kept the old
// host port in its proxy_pass, so the vhost was skipped and the domain stopped
// serving the app, and the compose was rendered with no domain. The edit now
// keeps the app's domain, as the GUI's edit (which always sends it) does.

// domainCatalog has one app whose compose names its domain, the way the real
// templates do (ROOT_URL: "https://{{ .Domain }}").
func domainCatalog(t *testing.T) *dockerapp.Catalog {
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

// movedPortRepo holds the app's port after the edit moved it to 10053.
type movedPortRepo struct{ *trackRepo }

func (movedPortRepo) ListPortsForApp(context.Context, string) ([]*models.DockerAppPublishedPort, error) {
	return []*models.DockerAppPublishedPort{{ID: "p2", AppID: "a1", PortName: "http", HostPort: 10053, ContainerPort: 8080,
		BindInterface: "loopback", Protocol: "tcp", ReverseProxy: true}}, nil
}

// editDomainRepo records the domain rows an edit creates and attaches.
type editDomainRepo struct {
	listingDomainRepo
	created  []*models.Domain
	attached map[string]models.NginxRules
}

func (r *editDomainRepo) Create(_ context.Context, d *models.Domain) error {
	r.created = append(r.created, d)
	return nil
}

func (r *editDomainRepo) AttachDockerApp(_ context.Context, id, _ string, rules models.NginxRules) error {
	if r.attached == nil {
		r.attached = map[string]models.NginxRules{}
	}
	r.attached[id] = rules
	return nil
}

func portsOnlyEditHandler(t *testing.T, domains ...models.Domain) (*dockerAppHandler, *editDomainRepo, *agent.MockClient) {
	t.Helper()
	mock := agent.NewMockClient()
	mock.On("docker_app.read_env", map[string]any{"env": map[string]string{}})
	mock.On("docker_app.install", map[string]any{"status": "running"})
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	repo := movedPortRepo{&trackRepo{app: &models.DockerApp{ID: "a1", Slug: "notes", Name: "notes", CatalogVersion: "1.0", Status: models.DockerAppStatusStopped}}}
	dom := &editDomainRepo{listingDomainRepo: listingDomainRepo{&fakeDomainRepo{owned: domains, byName: map[string]*models.Domain{}}}}
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Catalog: domainCatalog(t), Repo: repo, Agent: mock, Domains: dom}}
	return h, dom, mock
}

func TestEditDomainPorts_APortsOnlyEditKeepsTheManagedDomain(t *testing.T) {
	h, dom, mock := portsOnlyEditHandler(t, managedDomain("d1", "notes.example.com", "a1"))
	app, _ := h.cfg.Repo.FindByID(context.Background(), "a1")
	if err := h.editDomainPorts(context.Background(), app, nil, nil, "u1", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !dom.deleted["d1"] || len(dom.created) != 1 {
		t.Fatalf("deleted %v, created %d rows; want the domain row rebuilt", dom.deleted, len(dom.created))
	}
	got := dom.created[0]
	if got.Name != "notes.example.com" || got.ManagedBy != models.DomainManagedByDockerApp || got.DockerAppID == nil || *got.DockerAppID != "a1" {
		t.Fatalf("rebuilt row %+v, want notes.example.com managed by app a1", got)
	}
	if len(got.NginxRules) != 1 || got.NginxRules[0].Target != "http://127.0.0.1:10053" {
		t.Fatalf("rebuilt rules %+v, want proxy_pass to the app's new port 10053", got.NginxRules)
	}
	if v := vhostRemoves(t, mock); len(v) != 0 {
		t.Fatalf("vhost_remove %+v for the name the app keeps", v)
	}
	compose, _ := lastAgentParams(t, mock, "docker_app.install")["compose_yml"].(string)
	if !strings.Contains(compose, `URL: "https://notes.example.com"`) {
		t.Fatalf("compose lost the domain:\n%s", compose)
	}
}

// A tenant's own domain attached to the app gets its proxy_pass moved to the
// new port too.
func TestEditDomainPorts_APortsOnlyEditMovesAnAttachedDomain(t *testing.T) {
	appID := "a1"
	attached := models.Domain{ID: "w1", UserID: "u1", Name: "site.example.com", DockerAppID: &appID}
	h, dom, mock := portsOnlyEditHandler(t, attached)
	dom.byName["site.example.com"] = &attached
	app, _ := h.cfg.Repo.FindByID(context.Background(), "a1")
	if err := h.editDomainPorts(context.Background(), app, nil, nil, "u1", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	rules, ok := dom.attached["w1"]
	if !ok || len(rules) != 1 || rules[0].Target != "http://127.0.0.1:10053" {
		t.Fatalf("attached %v, want w1's proxy_pass on 10053", dom.attached)
	}
	if dom.deleted["w1"] || len(dom.created) != 0 {
		t.Fatalf("the tenant's domain was deleted (%v) or a row created (%d)", dom.deleted, len(dom.created))
	}
	compose, _ := lastAgentParams(t, mock, "docker_app.install")["compose_yml"].(string)
	if !strings.Contains(compose, `URL: "https://site.example.com"`) {
		t.Fatalf("compose lost the domain:\n%s", compose)
	}
}

// tenantAppRepo is movedPortRepo for a tenant's app, with the row write the
// PATCH makes before the edit.
type tenantAppRepo struct{ movedPortRepo }

func (tenantAppRepo) Update(context.Context, *models.DockerApp) error { return nil }

// An edit rebuilds the app's domain row and attributes it to the user the
// edit acts as. The admin PATCH passed the admin's own id, so an admin's save
// (the GUI always sends the domain) handed a tenant's app domain to the
// admin. It now acts for the app's owner with admin authority, as `jabali
// docker-app edit` does.
func TestAdminDockerPatch_KeepsATenantsDomainUnderTheTenant(t *testing.T) {
	h, dom, _ := portsOnlyEditHandler(t, managedDomain("d1", "notes.example.com", "a1"))
	owner, uname := "u1", "bob"
	repo := h.cfg.Repo.(movedPortRepo)
	repo.app.UserID = &owner
	h.cfg.Repo = tenantAppRepo{repo}
	h.cfg.Users = &fakeUserRepo{user: &models.User{ID: owner, Username: &uname}}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/admin/docker-apps/a1", strings.NewReader(`{"domain":"notes.example.com"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
	h.update(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(dom.created) != 1 || dom.created[0].UserID != owner {
		var got []string
		for _, d := range dom.created {
			got = append(got, d.UserID)
		}
		t.Fatalf("rebuilt domain rows owned by %v, want [%s]", got, owner)
	}
}

// An app with no domain gets none from a ports-only edit.
func TestEditDomainPorts_APortsOnlyEditAddsNoDomain(t *testing.T) {
	h, dom, _ := portsOnlyEditHandler(t, managedDomain("o1", "other.example.com", "a2"))
	app, _ := h.cfg.Repo.FindByID(context.Background(), "a1")
	if err := h.editDomainPorts(context.Background(), app, nil, nil, "u1", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(dom.created) != 0 || len(dom.attached) != 0 || len(dom.deleted) != 0 {
		t.Fatalf("created %d, attached %v, deleted %v; want no domain change", len(dom.created), dom.attached, dom.deleted)
	}
}
