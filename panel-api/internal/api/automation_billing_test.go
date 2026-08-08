package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes (extend the automation_write_routes_test.go set) ---

// abUsers is a richer user-repo fake for the billing endpoints: FindByID /
// FindByEmail / FindByUsername / Create / Update / Delete / List.
type abUsers struct {
	repository.UserRepository
	rows    map[string]*models.User
	created []*models.User
	deleted []string
}

func newAbUsers(users ...*models.User) *abUsers {
	m := map[string]*models.User{}
	for _, u := range users {
		m[u.ID] = u
	}
	return &abUsers{rows: m}
}

func (r *abUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := r.rows[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

func (r *abUsers) FindByEmail(_ context.Context, email string) (*models.User, error) {
	for _, u := range r.rows {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *abUsers) FindByUsername(_ context.Context, username string) (*models.User, error) {
	for _, u := range r.rows {
		if u.Username != nil && *u.Username == username {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *abUsers) Create(_ context.Context, u *models.User) error {
	for _, ex := range r.rows {
		if ex.Email == u.Email && u.Email != "" {
			return repository.ErrConflict
		}
		if ex.Username != nil && u.Username != nil && *ex.Username == *u.Username {
			return repository.ErrConflict
		}
	}
	r.rows[u.ID] = u
	r.created = append(r.created, u)
	return nil
}

func (r *abUsers) Update(_ context.Context, u *models.User) error {
	r.rows[u.ID] = u
	return nil
}

func (r *abUsers) Delete(_ context.Context, id string) error {
	delete(r.rows, id)
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *abUsers) List(_ context.Context, _ repository.ListOptions) ([]models.User, int64, error) {
	out := make([]models.User, 0, len(r.rows))
	for _, u := range r.rows {
		out = append(out, *u)
	}
	return out, int64(len(out)), nil
}

func (r *abUsers) LinkKratosIdentity(_ context.Context, _, _ string) error { return nil }

type abPackages struct {
	repository.PackageRepository
	rows map[string]*models.HostingPackage
}

func (r *abPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := r.rows[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

func (r *abPackages) List(_ context.Context, _ repository.ListOptions) ([]models.HostingPackage, int64, error) {
	out := make([]models.HostingPackage, 0, len(r.rows))
	for _, p := range r.rows {
		out = append(out, *p)
	}
	return out, int64(len(out)), nil
}

type abSnapshots struct {
	repository.DiskUsageSnapshotRepository
	rows map[string]*models.DiskUsageSnapshot
}

func (r *abSnapshots) Get(_ context.Context, userID string) (*models.DiskUsageSnapshot, error) {
	if s, ok := r.rows[userID]; ok {
		return s, nil
	}
	return nil, repository.ErrNotFound
}

func (r *abSnapshots) ListAll(_ context.Context) ([]models.DiskUsageSnapshot, error) {
	out := make([]models.DiskUsageSnapshot, 0, len(r.rows))
	for _, s := range r.rows {
		out = append(out, *s)
	}
	return out, nil
}

type abDomains struct {
	repository.DomainRepository
	byUser map[string][]models.Domain
}

func (r *abDomains) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	rows := r.byUser[userID]
	return rows, int64(len(rows)), nil
}

func (r *abDomains) List(_ context.Context, _ repository.ListOptions) ([]models.Domain, int64, error) {
	var out []models.Domain
	for _, rows := range r.byUser {
		out = append(out, rows...)
	}
	return out, int64(len(out)), nil
}

func (r *abDomains) Delete(_ context.Context, _ string) error { return nil }

type abBW struct {
	repository.BWDailyRepository
	sums map[string]uint64
}

func (r *abBW) SumByDomainIDs(_ context.Context, ids []string, _, _ time.Time) (map[string]uint64, error) {
	out := map[string]uint64{}
	for _, id := range ids {
		if v, ok := r.sums[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type abDockerApps struct {
	repository.DockerAppRepository
	apps []models.DockerApp
}

func (r *abDockerApps) ListByUserID(_ context.Context, _ string) ([]models.DockerApp, error) {
	return r.apps, nil
}

// --- helpers ---

func abRouter(cfg AutomationConfig, tok *models.AutomationToken) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	grp.Use(func(c *gin.Context) { c.Set("jabali_automation_token", tok); c.Next() })
	registerAutomationWrites(grp, cfg)
	return r
}

// abReq performs a request with a body stashed the way RequireAutomationHMAC
// would (the signed-bytes context key), so bindSigned sees it.
func abReq(r *gin.Engine, method, path, body string, tok *models.AutomationToken) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// abRouterWithBody injects both token and signed body for write handlers.
func abRouterWithBody(cfg AutomationConfig, tok *models.AutomationToken, body string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	grp.Use(func(c *gin.Context) {
		c.Set("jabali_automation_token", tok)
		if body != "" {
			c.Set("jabali_automation_signed_body", []byte(body))
		}
		c.Next()
	})
	registerAutomationWrites(grp, cfg)
	return r
}

func billingTok(scopes ...string) *models.AutomationToken {
	if len(scopes) == 0 {
		scopes = []string{"write:*", "delete:*", "read:*"}
	}
	return &models.AutomationToken{ID: "tokB", WritesEnabled: true, Scopes: models.AutomationScopes(scopes)}
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON body %q: %v", w.Body.String(), err)
	}
	return m
}

func billingCfg(users *abUsers) AutomationConfig {
	return AutomationConfig{
		Users:      users,
		Audits:     &awAudit{},
		BcryptCost: 4, // MinCost+ for fast tests
	}
}

// --- create ---

func TestAutomationUserCreate_HappyPath(t *testing.T) {
	users := newAbUsers()
	cfg := billingCfg(users)
	body := `{"email":"a@example.com","password":"longenough1"}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["ok"] != true || resp["status"] != "created" || resp["user_id"] == "" {
		t.Fatalf("bad create envelope: %v", resp)
	}
	if len(users.created) != 1 || users.created[0].IsAdmin {
		t.Fatalf("expected one non-admin user created, got %+v", users.created)
	}
}

func TestAutomationUserCreate_ShortPassword422(t *testing.T) {
	body := `{"email":"a@example.com","password":"short"}`
	r := abRouterWithBody(billingCfg(newAbUsers()), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", w.Code)
	}
}

func TestAutomationUserCreate_IsAdminFieldIgnored(t *testing.T) {
	// A hostile/buggy caller sending is_admin must never mint an admin.
	users := newAbUsers()
	body := `{"email":"a@example.com","password":"longenough1","is_admin":true}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", w.Code)
	}
	if users.created[0].IsAdmin {
		t.Fatal("automation create must force is_admin=false")
	}
}

func TestAutomationUserCreate_IdempotentExactRetry(t *testing.T) {
	un := "existing"
	users := newAbUsers(&models.User{ID: "u9", Email: "a@example.com", Username: &un})
	body := `{"email":"a@example.com","password":"longenough1","username":"existing"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("exact retry: want 200 exists, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["status"] != "exists" || resp["user_id"] != "u9" {
		t.Fatalf("bad exists envelope: %v", resp)
	}
}

func TestAutomationUserCreate_PartialMatchConflict(t *testing.T) {
	// Same email, DIFFERENT username → 409, never "exists".
	un := "otheruser"
	users := newAbUsers(&models.User{ID: "u9", Email: "a@example.com", Username: &un})
	body := `{"email":"a@example.com","password":"longenough1","username":"newuser"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("partial match: want 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAutomationUserCreate_ScopeDenied(t *testing.T) {
	body := `{"email":"a@example.com","password":"longenough1"}`
	r := abRouterWithBody(billingCfg(newAbUsers()), billingTok("read:*"), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok("read:*"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for read-only token, got %d", w.Code)
	}
}

// --- password ---

func TestAutomationUserPassword_AdminRefused(t *testing.T) {
	users := newAbUsers(&models.User{ID: "adm", Email: "root@x", IsAdmin: true})
	body := `{"password":"longenough1"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/adm/password", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("admin password change: want 409, got %d", w.Code)
	}
}

func TestAutomationUserPassword_NoKratosIdentity409(t *testing.T) {
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un})
	body := `{"password":"longenough1"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/u1/password", body, billingTok())
	// No KratosIdentityID on the row → userops.ErrNoKratosIdentity → conflict.
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 for identity-less user, got %d: %s", w.Code, w.Body.String())
	}
}

// --- package ---

func TestAutomationUserPackage_AssignsAndValidates(t *testing.T) {
	un := "bob"
	u := &models.User{ID: "u1", Email: "b@x.com", Username: &un}
	users := newAbUsers(u)
	pkgs := &abPackages{rows: map[string]*models.HostingPackage{"p1": {ID: "p1", Name: "Basic"}}}
	cfg := billingCfg(users)
	cfg.Packages = pkgs

	body := `{"package_id":"p1"}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPut, "/users/u1/package", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if u.PackageID == nil || *u.PackageID != "p1" {
		t.Fatalf("package not assigned: %+v", u.PackageID)
	}

	body404 := `{"package_id":"ghost"}`
	r2 := abRouterWithBody(cfg, billingTok(), body404)
	w2 := abReq(r2, http.MethodPut, "/users/u1/package", body404, billingTok())
	if w2.Code != http.StatusNotFound {
		t.Fatalf("unknown package: want 404, got %d", w2.Code)
	}
}

// --- delete ---

func TestAutomationUserDelete_RequiresConfirm(t *testing.T) {
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un})
	body := `{}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodDelete, "/users/u1", body, billingTok())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing confirm: want 422, got %d", w.Code)
	}
	if len(users.deleted) != 0 {
		t.Fatal("must not delete without confirm")
	}
}

func TestAutomationUserDelete_AdminRefused(t *testing.T) {
	users := newAbUsers(&models.User{ID: "adm", Email: "root@x", IsAdmin: true})
	body := `{"confirm":true}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodDelete, "/users/adm", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("admin delete: want 409, got %d", w.Code)
	}
}

func TestAutomationUserDelete_DryRunTouchesNothing(t *testing.T) {
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un})
	doms := &abDomains{byUser: map[string][]models.Domain{"u1": {{ID: "d1", UserID: "u1"}, {ID: "d2", UserID: "u1"}}}}
	cfg := billingCfg(users)
	cfg.Domains = doms

	body := `{"dry_run":true}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodDelete, "/users/u1", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("dry run: want 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["status"] != "dry_run" {
		t.Fatalf("bad dry-run envelope: %v", resp)
	}
	preview := resp["preview"].(map[string]any)
	if preview["domains"] != float64(2) {
		t.Fatalf("expected 2 domains in preview, got %v", preview)
	}
	if len(users.deleted) != 0 {
		t.Fatal("dry run must not delete")
	}
}

func TestAutomationUserDelete_ConfirmDeletes(t *testing.T) {
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un})
	cfg := billingCfg(users)
	body := `{"confirm":true}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodDelete, "/users/u1", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(users.deleted) != 1 || users.deleted[0] != "u1" {
		t.Fatalf("expected u1 deleted, got %v", users.deleted)
	}
}

func TestAutomationUserDelete_ScopeWriteInsufficient(t *testing.T) {
	// write:* must NOT satisfy delete:users (ADR-0157 family separation).
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un})
	body := `{"confirm":true}`
	tok := billingTok("write:*")
	r := abRouterWithBody(billingCfg(users), tok, body)
	w := abReq(r, http.MethodDelete, "/users/u1", body, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("write:* on delete route: want 403, got %d", w.Code)
	}
}

// --- usage + packages reads ---

func TestAutomationUsage_PerUserComposition(t *testing.T) {
	un := "bob"
	pid := "p1"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un, PackageID: &pid})
	cfg := billingCfg(users)
	cfg.DiskSnapshots = &abSnapshots{rows: map[string]*models.DiskUsageSnapshot{
		"u1": {UserID: "u1", Payload: `{"total_bytes":1073741824,"quota_bytes":0}`, ComputedAt: time.Now()},
	}}
	cfg.Domains = &abDomains{byUser: map[string][]models.Domain{"u1": {{ID: "d1", UserID: "u1"}}}}
	cfg.BWDaily = &abBW{sums: map[string]uint64{"d1": 2 * 1024 * 1024 * 1024}}
	cfg.Packages = &abPackages{rows: map[string]*models.HostingPackage{
		"p1": {ID: "p1", Name: "Basic", DiskQuotaMB: 10240, BandwidthQuotaMB: 102400},
	}}

	r := abRouterWithBody(cfg, billingTok(), "")
	w := abReq(r, http.MethodGet, "/users/u1/usage", "", billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	data := resp["data"].(map[string]any)
	if data["disk_used_mb"] != float64(1024) {
		t.Fatalf("disk_used_mb: want 1024, got %v", data["disk_used_mb"])
	}
	if data["bw_used_mb"] != float64(2048) {
		t.Fatalf("bw_used_mb: want 2048, got %v", data["bw_used_mb"])
	}
	if data["disk_quota_mb"] != float64(10240) || data["bw_quota_mb"] != float64(102400) {
		t.Fatalf("quotas from package: got %v / %v", data["disk_quota_mb"], data["bw_quota_mb"])
	}
}

func TestAutomationUsage_BulkSkipsAdmins(t *testing.T) {
	un := "bob"
	users := newAbUsers(
		&models.User{ID: "u1", Email: "b@x.com", Username: &un},
		&models.User{ID: "adm", Email: "root@x", IsAdmin: true},
	)
	cfg := billingCfg(users)
	cfg.DiskSnapshots = &abSnapshots{rows: map[string]*models.DiskUsageSnapshot{}}

	r := abRouterWithBody(cfg, billingTok(), "")
	w := abReq(r, http.MethodGet, "/usage", "", billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	resp := decodeBody(t, w)
	if resp["total"] != float64(1) {
		t.Fatalf("bulk usage must skip admins: %v", resp)
	}
}

func TestAutomationPackages_ListAndScope(t *testing.T) {
	cfg := billingCfg(newAbUsers())
	cfg.Packages = &abPackages{rows: map[string]*models.HostingPackage{
		"p1": {ID: "p1", Name: "Basic", DiskQuotaMB: 10240},
	}}

	r := abRouterWithBody(cfg, billingTok("read:packages"), "")
	w := abReq(r, http.MethodGet, "/packages", "", billingTok("read:packages"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["total"] != float64(1) {
		t.Fatalf("expected one package, got %v", resp)
	}

	// read:users must NOT satisfy read:packages (exact-scope matching).
	tok := billingTok("read:users")
	r2 := abRouterWithBody(cfg, tok, "")
	w2 := abReq(r2, http.MethodGet, "/packages", "", tok)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("read:users on /packages: want 403, got %d", w2.Code)
	}
}

func TestAutomationCapabilities_BillingActionsAdvertised(t *testing.T) {
	cfg := billingCfg(newAbUsers())
	cfg.Packages = &abPackages{rows: map[string]*models.HostingPackage{}}
	cfg.DiskSnapshots = &abSnapshots{rows: map[string]*models.DiskUsageSnapshot{}}

	r := abRouterWithBody(cfg, billingTok(), "")
	w := abReq(r, http.MethodGet, "/capabilities", "", billingTok())
	var body struct {
		Actions []capability `json:"actions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	has := map[string]bool{}
	for _, a := range body.Actions {
		has[a.Action] = true
	}
	for _, want := range []string{"users.create", "users.password", "users.package", "users.delete", "usage.read", "packages.read"} {
		if !has[want] {
			t.Fatalf("capability %s missing: %v", want, body.Actions)
		}
	}
}
