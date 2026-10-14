package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A domain row a Docker app created is the only thing that names its proxy
// vhost. Every path that drops such a row for good removes the vhost and the
// certificate files with it (docker_app.vhost_remove, purge_certificates):
// left behind, the vhost keeps proxying to a loopback port the panel hands to
// the next app installed, and its Let's Encrypt lineage keeps renewing. A row
// recreated under the same name at once keeps both.

// listingDomainRepo adds List (the admin paths list every domain) to the
// tenant tests' fake.
type listingDomainRepo struct{ *fakeDomainRepo }

func (f listingDomainRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.owned, int64(len(f.owned)), nil
}

type vhostRemoveCall struct {
	DomainName        string `json:"domain_name"`
	PurgeCertificates bool   `json:"purge_certificates"`
}

func vhostRemoves(t *testing.T, mock *agent.MockClient) []vhostRemoveCall {
	t.Helper()
	var out []vhostRemoveCall
	for _, c := range mock.Calls() {
		if c.Command != "docker_app.vhost_remove" {
			continue
		}
		var v vhostRemoveCall
		if err := json.Unmarshal(c.Params, &v); err != nil {
			t.Fatalf("decode vhost_remove params: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func wantPurged(t *testing.T, mock *agent.MockClient, names ...string) {
	t.Helper()
	got := vhostRemoves(t, mock)
	if len(got) != len(names) {
		t.Fatalf("vhost_remove calls %+v, want a purge of %v", got, names)
	}
	for i, n := range names {
		if got[i].DomainName != n || !got[i].PurgeCertificates {
			t.Fatalf("vhost_remove call %d = %+v, want %s with purge_certificates", i, got[i], n)
		}
	}
}

func managedDomain(id, name, appID string) models.Domain {
	return models.Domain{ID: id, UserID: "u1", Name: name, ManagedBy: models.DomainManagedByDockerApp, DockerAppID: &appID}
}

func TestRemoveManagedDockerDomain_DeletesTheRowAndPurges(t *testing.T) {
	dom := &fakeDomainRepo{}
	mock := agent.NewMockClient()
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})

	RemoveManagedDockerDomain(context.Background(), dom, mock, managedDomain("d1", "app.example.com", "a1"))

	if !dom.deleted["d1"] {
		t.Fatal("the domain row was not deleted")
	}
	wantPurged(t, mock, "app.example.com")
}

func TestTenantDocker_DeletePurgesTheManagedDomain(t *testing.T) {
	appID := "a1"
	dom := &fakeDomainRepo{owned: []models.Domain{
		{ID: "d1", UserID: "u1", DockerAppID: &appID, ManagedBy: "", Name: "mine.example.com"},
		managedDomain("d2", "auto.example.com", appID),
	}}
	repo := &fakeDockerRepo{owned: map[string]*models.DockerApp{
		appID: {ID: appID, UserID: uname("u1"), Slug: "tdemo", Status: models.DockerAppStatusRunning},
	}}
	mock := agent.NewMockClient()
	mock.On("docker_app.delete", map[string]any{"ok": true})
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	r := tenantRouter(t, UserDockerAppHandlerConfig{Repo: repo, Catalog: tenantCatalog(t), Domains: dom, Agent: mock}, true)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/docker-apps/a1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	// The tenant's own domain is only detached; its vhost and certificate stay.
	wantPurged(t, mock, "auto.example.com")
}

// deletingTrackRepo lets the admin delete remove the app row.
type deletingTrackRepo struct{ *trackRepo }

func (deletingTrackRepo) Delete(context.Context, string) error { return nil }

func TestAdminDocker_DeletePurgesTheManagedDomain(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.3")
	mock.On("docker_app.delete", map[string]any{"ok": true})
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	dom := &fakeDomainRepo{owned: []models.Domain{
		{ID: "mine", UserID: "u1", DockerAppID: &repo.app.ID, ManagedBy: "", Name: "mine.example.com"},
		managedDomain("auto", "auto.example.com", repo.app.ID),
		managedDomain("other", "other.example.com", "a2"),
	}}
	h.cfg.Repo = deletingTrackRepo{repo}
	h.cfg.Domains = listingDomainRepo{dom}

	// 204, which a bare test context records as 200 (no body written).
	if w := trackRequest(t, h.delete, http.MethodDelete, ""); w.Code >= 300 {
		t.Fatalf("status %d body %s, want 204", w.Code, w.Body)
	}
	if !dom.deleted["auto"] || dom.deleted["mine"] || dom.deleted["other"] {
		t.Fatalf("deleted %v, want only the app's managed domain", dom.deleted)
	}
	wantPurged(t, mock, "auto.example.com")
}

func TestEditDomainPorts_ANameTheAppDropsGoesWithItsVhost(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.3")
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	dom := &fakeDomainRepo{owned: []models.Domain{
		managedDomain("old", "old.example.com", repo.app.ID),
		managedDomain("other", "other.example.com", "a2"),
	}}
	h.cfg.Domains = listingDomainRepo{dom}

	newDomain := "new.example.com"
	if err := h.editDomainPorts(context.Background(), repo.app, &newDomain, nil, "admin", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !dom.deleted["old"] || dom.deleted["other"] {
		t.Fatalf("deleted %v, want only the app's old domain", dom.deleted)
	}
	wantPurged(t, mock, "old.example.com")
}

func TestEditDomainPorts_TheSameNameKeepsItsVhostAndCertificate(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.3")
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	dom := &fakeDomainRepo{owned: []models.Domain{managedDomain("same", "same.example.com", repo.app.ID)}}
	h.cfg.Domains = listingDomainRepo{dom}

	sameDomain := "Same.example.com "
	if err := h.editDomainPorts(context.Background(), repo.app, &sameDomain, nil, "admin", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("vhost_remove %+v for a name the app keeps", got)
	}
}

func TestClearDockerAppCorpse_PurgesOnlyNamesNotReused(t *testing.T) {
	mock := agent.NewMockClient()
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	dom := &fakeDomainRepo{}
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Repo: &fakeDockerRepo{}, Domains: dom, Agent: mock}}
	scope := []models.Domain{
		managedDomain("reuse", "reuse.example.com", "a1"),
		managedDomain("gone", "gone.example.com", "a1"),
		managedDomain("other", "other.example.com", "a2"),
	}

	if err := h.clearDockerAppCorpse(context.Background(), &models.DockerApp{ID: "a1"}, scope, "Reuse.example.com"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !dom.deleted["reuse"] || !dom.deleted["gone"] || dom.deleted["other"] {
		t.Fatalf("deleted %v, want the corpse's two domains", dom.deleted)
	}
	wantPurged(t, mock, "gone.example.com")
}
