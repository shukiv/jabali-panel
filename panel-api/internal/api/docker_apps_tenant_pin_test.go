package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/tenantcompose"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// tdemoServices is the service set the tenantCatalog "tdemo" template pins.
var tdemoServices = tenantcompose.Services{
	"tdemo": {Image: "docker.io/library/busybox:1@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
}

// pinnedServicesParam decodes the tenant_services param an agent call carried.
func pinnedServicesParam(t *testing.T, p map[string]any) tenantcompose.Services {
	t.Helper()
	raw, ok := p["tenant_services"]
	if !ok {
		t.Fatalf("agent call carries no tenant_services: %v", p)
	}
	b, _ := json.Marshal(raw)
	var s tenantcompose.Services
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode tenant_services: %v", err)
	}
	return s
}

// attachingDomainRepo lets the install attach the app to the tenant's own
// domain.
type attachingDomainRepo struct{ fakeDomainRepo }

func (f *attachingDomainRepo) AttachDockerApp(context.Context, string, string, models.NginxRules) error {
	return nil
}

// installingDockerRepo answers the install's closing re-read of the new row.
type installingDockerRepo struct{ *fakeDockerRepo }

func (f *installingDockerRepo) FindByID(context.Context, string) (*models.DockerApp, error) {
	return nil, repository.ErrNotFound
}

// GH #1903: the tenant install sends the agent the service set rendered
// without tenant input, next to the rest of the tenant gate.
func TestTenantDocker_InstallSendsPinnedServiceSet(t *testing.T) {
	mock := agent.NewMockClient()
	mock.On("docker_app.install", map[string]any{"status": "running"})
	cfg := dockerCgroupCfg(t, mock, models.DockerAppStatusStopped)
	cfg.Repo = &installingDockerRepo{cfg.Repo.(*fakeDockerRepo)}
	cfg.Domains = &attachingDomainRepo{fakeDomainRepo{byName: map[string]*models.Domain{"x.example.com": {ID: "d1", UserID: "u1", Name: "x.example.com"}}}}
	r := tenantRouter(t, cfg, true)

	rec := post(r, `{"slug":"tdemo","name":"x","domain":"x.example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("install = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, c := range mock.Calls() {
			if c.Command != "docker_app.install" {
				continue
			}
			var p map[string]any
			if err := json.Unmarshal(c.Params, &p); err != nil {
				t.Fatal(err)
			}
			if p["tenant_validate"] != true || p["tenant_cgroup"] != "jabali-user-alice.slice" {
				t.Fatalf("install lost the tenant gate: %v", p)
			}
			if err := tdemoServices.Check(pinnedServicesParam(t, p)); err != nil {
				t.Fatalf("install sent the wrong service set: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no docker_app.install dispatched; calls=%v", mock.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRenderInstallCompose_PinsOnlyTenantApps(t *testing.T) {
	uid := "u1"
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{
		Catalog: tenantCatalog(t),
		Users:   &fakeUserRepo{user: &models.User{ID: uid, Username: uname("alice")}},
		Repo:    &fakeDockerRepo{},
	}}
	ctx := context.Background()

	_, _, services, err := h.renderInstallCompose(ctx, &models.DockerApp{ID: "a1", Slug: "tdemo", UserID: &uid}, "", map[string]string{})
	if err != nil {
		t.Fatalf("tenant re-render: %v", err)
	}
	if err := tdemoServices.Check(services); err != nil {
		t.Fatalf("tenant re-render must return the pinned set: %v", err)
	}

	_, _, services, err = h.renderInstallCompose(ctx, &models.DockerApp{ID: "a2", Slug: "tdemo"}, "", map[string]string{})
	if err != nil {
		t.Fatalf("admin re-render: %v", err)
	}
	if services != nil {
		t.Fatalf("an admin app is not pinned, got %v", services)
	}
}

// The env edit door re-renders and recreates the app through docker_app.update;
// it must carry the pinned set, or the agent refuses the write.
func TestApplyEnv_TenantSendsPinnedServiceSet(t *testing.T) {
	uid := "u1"
	mock := agent.NewMockClient()
	mock.On("docker_app.update", map[string]any{"outcome": "updated"})
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{
		Catalog: tenantCatalog(t),
		Users:   &fakeUserRepo{user: &models.User{ID: uid, Username: uname("alice")}},
		Repo:    &fakeDockerRepo{},
		Agent:   mock,
	}}
	if err := h.applyEnv(context.Background(), &models.DockerApp{ID: "a1", Slug: "tdemo", UserID: &uid}, map[string]string{}); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	p := lastAgentParams(t, mock, "docker_app.update")
	if p["tenant_validate"] != true || p["compose_yml"] == nil {
		t.Fatalf("env edit lost the tenant gate or the compose: %v", p)
	}
	if err := tdemoServices.Check(pinnedServicesParam(t, p)); err != nil {
		t.Fatalf("env edit sent the wrong service set: %v", err)
	}
}
