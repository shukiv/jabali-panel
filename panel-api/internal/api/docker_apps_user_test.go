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
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- pure helpers ---

func TestTenantInstanceSlug_NamespacedByOwner(t *testing.T) {
	a := tenantInstanceSlug("memos", "01HXYZABCDEFGHIJKLMNOPQRST", "notes")
	b := tenantInstanceSlug("memos", "01HXYZ99999999999999999999", "notes")
	if a == b {
		t.Fatal("same slug+name for different owners must differ")
	}
	if !strings.HasPrefix(a, "memos-") || strings.ToLower(a) != a {
		t.Errorf("instance slug %q not lowercased/prefixed", a)
	}
	// Regression: ULID user IDs share a millisecond-timestamp PREFIX, so two
	// users created in the same window must NOT collide (the userID[:8] bug).
	// These two differ only in their random tail.
	p1 := tenantInstanceSlug("memos", "01HXYZ0000AAAAAAAAAAAAAAAA", "notes")
	p2 := tenantInstanceSlug("memos", "01HXYZ0000BBBBBBBBBBBBBBBB", "notes")
	if p1 == p2 {
		t.Fatalf("same-timestamp-prefix users collided: %q == %q (entropy must come from the whole ID)", p1, p2)
	}
}

func TestTenantInstallable_Filter(t *testing.T) {
	if tenantInstallable(dockerapp.Entry{TenantInstallable: false}) {
		t.Error("false flag must not be installable")
	}
	pub := dockerapp.Entry{TenantInstallable: true, Ports: []dockerapp.PortSpec{{DefaultBind: "public"}}}
	if tenantInstallable(pub) {
		t.Error("a default-public port must disqualify a tenant app")
	}
	ok := dockerapp.Entry{TenantInstallable: true, Ports: []dockerapp.PortSpec{{DefaultBind: "loopback"}}}
	if !tenantInstallable(ok) {
		t.Error("loopback-only tenant_installable app must be installable")
	}
}

// --- fakes (embed the interface; override only what the guard paths hit) ---

type fakeDockerRepo struct {
	repository.DockerAppRepository
	count    int64
	sumBytes int64
	owned    map[string]*models.DockerApp
	updated  map[string]string
}

func (f *fakeDockerRepo) UpdateStatus(_ context.Context, id, status string, _ *string) error {
	if f.updated == nil {
		f.updated = map[string]string{}
	}
	f.updated[id] = status
	return nil
}

func (f *fakeDockerRepo) SumDataBytesByUserID(context.Context, string) (int64, error) {
	return f.sumBytes, nil
}

func (f *fakeDockerRepo) CountByUserID(context.Context, string) (int64, error) { return f.count, nil }
func (f *fakeDockerRepo) ListByUserID(context.Context, string) ([]*models.DockerApp, error) {
	return nil, nil
}
func (f *fakeDockerRepo) FindByIDForUser(_ context.Context, id, _ string) (*models.DockerApp, error) {
	if a, ok := f.owned[id]; ok {
		return a, nil
	}
	return nil, gorm.ErrRecordNotFound
}

type fakeUserRepo struct {
	repository.UserRepository
	user *models.User
}

func (f *fakeUserRepo) FindByID(context.Context, string) (*models.User, error) { return f.user, nil }

type fakePkgRepo struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f *fakePkgRepo) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, nil
}

type fakeDomainRepo struct {
	repository.DomainRepository
	byName   map[string]*models.Domain
	owned    []models.Domain
	detached map[string]bool
	deleted  map[string]bool
}

func (f *fakeDomainRepo) ListByUserID(_ context.Context, _ string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	return f.owned, int64(len(f.owned)), nil
}

func (f *fakeDomainRepo) DetachDockerApp(_ context.Context, id string, _ bool) error {
	if f.detached == nil {
		f.detached = map[string]bool{}
	}
	f.detached[id] = true
	return nil
}

func (f *fakeDomainRepo) Delete(_ context.Context, id string) error {
	if f.deleted == nil {
		f.deleted = map[string]bool{}
	}
	f.deleted[id] = true
	return nil
}

func (f *fakeDomainRepo) FindByName(_ context.Context, n string) (*models.Domain, error) {
	if d, ok := f.byName[n]; ok {
		return d, nil
	}
	return nil, gorm.ErrRecordNotFound
}

// tenantCatalog writes a minimal tenant_installable app into a temp dir.
func tenantCatalog(t *testing.T) *dockerapp.Catalog {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "tdemo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	app := `slug: tdemo
name: TDemo
version: "1.0.0"
description: tenant demo app
image_channel: docker.io/library/busybox:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
tenant_installable: true
tenant_caps: ["CHOWN"]
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
	compose := "services:\n  tdemo:\n    image: {{ .ImageChannel }}\n"
	os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(app), 0o644)
	os.WriteFile(filepath.Join(dir, "compose.yml.tmpl"), []byte(compose), 0o644)
	os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg/>`), 0o644)
	cat, errs := dockerapp.LoadDir(root)
	if len(errs) > 0 {
		t.Fatalf("temp catalog load errors: %v", errs)
	}
	return cat
}

func tenantRouter(t *testing.T, cfg UserDockerAppHandlerConfig, flagExists bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if flagExists {
		f := filepath.Join(t.TempDir(), "flag")
		os.WriteFile(f, []byte("on"), 0o644)
		cfg.TenantFlagPath = f
	} else {
		cfg.TenantFlagPath = filepath.Join(t.TempDir(), "absent")
	}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})
		c.Next()
	})
	RegisterUserDockerAppRoutes(v1, cfg)
	return r
}

func post(r *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docker-apps", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func uname(s string) *string { return &s }

func TestTenantDocker_FlagAbsent403(t *testing.T) {
	r := tenantRouter(t, UserDockerAppHandlerConfig{Repo: &fakeDockerRepo{}, Catalog: tenantCatalog(t)}, false)
	rec := post(r, `{"slug":"tdemo","name":"x","domain":"x.example.com"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "docker_tenant_not_enabled") {
		t.Fatalf("want 403 docker_tenant_not_enabled, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_PackageNotIncluded403(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{
		Repo:     &fakeDockerRepo{},
		Catalog:  tenantCatalog(t),
		Users:    &fakeUserRepo{user: &models.User{ID: "u1", Username: uname("alice"), PackageID: uname("p1")}},
		Packages: &fakePkgRepo{pkg: &models.HostingPackage{ID: "p1", MaxDockerApps: 0}},
		Domains:  &fakeDomainRepo{},
	}
	r := tenantRouter(t, cfg, true)
	rec := post(r, `{"slug":"tdemo","name":"x","domain":"x.example.com"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "docker_apps_not_in_package") {
		t.Fatalf("want 403 not_in_package, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_QuotaExceeded409(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{
		Repo:     &fakeDockerRepo{count: 1},
		Catalog:  tenantCatalog(t),
		Users:    &fakeUserRepo{user: &models.User{ID: "u1", Username: uname("alice"), PackageID: uname("p1")}},
		Packages: &fakePkgRepo{pkg: &models.HostingPackage{ID: "p1", MaxDockerApps: 1}},
		Domains:  &fakeDomainRepo{},
	}
	r := tenantRouter(t, cfg, true)
	rec := post(r, `{"slug":"tdemo","name":"x","domain":"x.example.com"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "docker_app_quota_exceeded") {
		t.Fatalf("want 409 quota_exceeded, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_ForeignDomain409(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{
		Repo:     &fakeDockerRepo{count: 0},
		Catalog:  tenantCatalog(t),
		Users:    &fakeUserRepo{user: &models.User{ID: "u1", Username: uname("alice"), PackageID: uname("p1")}},
		Packages: &fakePkgRepo{pkg: &models.HostingPackage{ID: "p1", MaxDockerApps: 5}},
		Domains:  &fakeDomainRepo{byName: map[string]*models.Domain{"x.example.com": {ID: "d1", UserID: "someone-else"}}},
	}
	r := tenantRouter(t, cfg, true)
	rec := post(r, `{"slug":"tdemo","name":"x","domain":"x.example.com"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "domain_in_use") {
		t.Fatalf("want 409 domain_in_use, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_UnknownSlug400(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{Repo: &fakeDockerRepo{}, Catalog: tenantCatalog(t)}
	r := tenantRouter(t, cfg, true)
	rec := post(r, `{"slug":"nope","name":"x","domain":"x.example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown slug, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_GetNotOwned404(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{Repo: &fakeDockerRepo{owned: map[string]*models.DockerApp{}}, Catalog: tenantCatalog(t)}
	r := tenantRouter(t, cfg, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docker-apps/someone-elses-id", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for non-owned app, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_CatalogFilteredToInstallable(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{Repo: &fakeDockerRepo{}, Catalog: tenantCatalog(t)}
	r := tenantRouter(t, cfg, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docker-apps/catalog", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "tdemo") {
		t.Fatalf("tenant catalog should list tdemo: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_LogsEnvNotOwned404(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{Repo: &fakeDockerRepo{owned: map[string]*models.DockerApp{}}, Catalog: tenantCatalog(t)}
	r := tenantRouter(t, cfg, true)
	for _, path := range []string{"/api/v1/docker-apps/x/logs", "/api/v1/docker-apps/x/env"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404 for non-owned, got %d", path, rec.Code)
		}
	}
}

func TestTenantDocker_UsageOverQuota(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{
		Repo:     &fakeDockerRepo{sumBytes: 600 * 1024 * 1024}, // 600 MiB used
		Catalog:  tenantCatalog(t),
		Users:    &fakeUserRepo{user: &models.User{ID: "u1", Username: uname("alice"), PackageID: uname("p1")}},
		Packages: &fakePkgRepo{pkg: &models.HostingPackage{ID: "p1", DiskQuotaMB: 500}}, // 500 MiB quota
	}
	r := tenantRouter(t, cfg, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docker-apps/usage", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"over_quota":true`) {
		t.Fatalf("want over_quota true (600>500 MiB): %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantDocker_UsageUnderQuota(t *testing.T) {
	cfg := UserDockerAppHandlerConfig{
		Repo:     &fakeDockerRepo{sumBytes: 100 * 1024 * 1024},
		Catalog:  tenantCatalog(t),
		Users:    &fakeUserRepo{user: &models.User{ID: "u1", Username: uname("alice"), PackageID: uname("p1")}},
		Packages: &fakePkgRepo{pkg: &models.HostingPackage{ID: "p1", DiskQuotaMB: 500}},
	}
	r := tenantRouter(t, cfg, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docker-apps/usage", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"over_quota":false`) {
		t.Fatalf("want over_quota false: %d %s", rec.Code, rec.Body.String())
	}
}

// GH #284: tenant delete must detach the app's own domain link + clear the
// injected proxy_pass (else the hostname keeps 502-ing at the dead container)
// and remove any hostname auto-created for the app — mirroring the admin path.
func TestTenantDocker_DeleteCleansDomainLinks(t *testing.T) {
	appID := "a1"
	dom := &fakeDomainRepo{
		owned: []models.Domain{
			{ID: "d1", UserID: "u1", DockerAppID: &appID, ManagedBy: ""},
			{ID: "d2", UserID: "u1", DockerAppID: &appID, ManagedBy: models.DomainManagedByDockerApp, Name: "auto.example.com"},
		},
	}
	repo := &fakeDockerRepo{owned: map[string]*models.DockerApp{
		appID: {ID: appID, UserID: uname("u1"), Slug: "tdemo", Status: models.DockerAppStatusFailed},
	}}
	cfg := UserDockerAppHandlerConfig{Repo: repo, Catalog: tenantCatalog(t), Domains: dom}
	r := tenantRouter(t, cfg, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/docker-apps/a1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if !dom.detached["d1"] {
		t.Errorf("tenant-owned domain d1 should be detached (proxy_pass cleared)")
	}
	if !dom.deleted["d2"] {
		t.Errorf("auto-managed domain d2 should be deleted")
	}
	if repo.updated["a1"] != models.DockerAppStatusDeleted {
		t.Errorf("app should be soft-deleted, got %q", repo.updated["a1"])
	}
}
