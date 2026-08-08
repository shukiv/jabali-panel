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

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
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
	// Mirror the real schema: username carries a unique index; email is
	// deliberately NOT unique post-M54 (one client, many accounts).
	for _, ex := range r.rows {
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

func TestAutomationUserCreate_SameEmailNewUsernameCreatesSecondAccount(t *testing.T) {
	// Email is NOT unique post-M54 (live-verified on mx 2026-08-08): one
	// billing client may own several hosting accounts. Same email +
	// different username is a legitimate second create, not a conflict.
	un := "otheruser"
	users := newAbUsers(&models.User{ID: "u9", Email: "a@example.com", Username: &un})
	body := `{"email":"a@example.com","password":"longenough1","username":"newuser"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusCreated {
		t.Fatalf("second account for same email: want 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAutomationUserCreate_UsernameCollisionDifferentEmail409(t *testing.T) {
	// Same username but a DIFFERENT email must stay a conflict — the
	// exists-retry path may never adopt someone else's account.
	un := "sameuser"
	users := newAbUsers(&models.User{ID: "u9", Email: "owner@example.com", Username: &un})
	body := `{"email":"attacker@example.com","password":"longenough1","username":"sameuser"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("username collision with different email: want 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAutomationUserCreate_EmptyEmailUsernameCollision409(t *testing.T) {
	// Account-adoption guard: a create with NO email that collides on an
	// existing username must be a 409 conflict, never a 200 "exists"
	// pointing at that unrelated user — a billing panel must not be bound
	// to the wrong tenant.
	un := "victim"
	users := newAbUsers(&models.User{ID: "vuln", Email: "victim@example.com", Username: &un})
	body := `{"username":"victim","password":"longenough1"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("empty-email username collision: want 409, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["status"] == "exists" {
		t.Fatalf("must never return 'exists' for an empty-email collision: %v", resp)
	}
}

func TestAutomationUserCreate_ExistsRetryWithDerivedUsername(t *testing.T) {
	// Retry that supplies only the email must still resolve "exists" via
	// the SAME username derivation userops.Create uses.
	derived := "derivedbob"
	users := newAbUsers(&models.User{ID: "u7", Email: "derivedbob@example.com", Username: &derived})
	body := `{"email":"derivedbob@example.com","password":"longenough1"}`
	r := abRouterWithBody(billingCfg(users), billingTok(), body)
	w := abReq(r, http.MethodPost, "/users", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("email-only exact retry: want 200 exists, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["status"] != "exists" || resp["user_id"] != "u7" {
		t.Fatalf("bad exists envelope: %v", resp)
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

// --- login token (ADR-0165) ---

// kratosProbe records what the handler sent to Kratos so tests can assert
// on the actual request (not just the echoed response).
type kratosProbe struct {
	calls     int
	lastExpIn string // the expires_in field the handler asked Kratos for
	status    int    // response status to return (0 → 201 created)
}

// fakeKratosAdmin serves POST /admin/recovery/code the way Kratos does,
// so the handler runs against the real kratosclient wrapper.
func fakeKratosAdmin(t *testing.T) (*httptest.Server, *kratosProbe) {
	t.Helper()
	p := &kratosProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/recovery/code" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		p.calls++
		var body struct {
			ExpiresIn string `json:"expires_in"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.lastExpIn = body.ExpiresIn
		if p.status != 0 {
			w.WriteHeader(p.status)
			_, _ = w.Write([]byte(`{"error":{"message":"kratos boom"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		// Kratos's code strategy returns the link WITHOUT the code embedded
		// (the code is a separate out-of-band field). JAB-232: the handler must
		// fold recovery_code into the URL so the SPA /recovery page can redeem.
		_, _ = w.Write([]byte(`{"recovery_link":"https://panel.example.com:8443/recovery?flow=f1","recovery_code":"SECRETCODE","expires_at":"2026-08-08T00:05:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, p
}

// auditRows exposes the recorded audit events for leak assertions.
func auditRows(cfg AutomationConfig) []*models.AuditEvent {
	if a, ok := cfg.Audits.(*awAudit); ok {
		return a.rows
	}
	return nil
}

func TestAutomationLoginToken_HappyPath(t *testing.T) {
	srv, probe := fakeKratosAdmin(t)
	kid := "11111111-2222-3333-4444-555555555555"
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un, KratosIdentityID: &kid})
	cfg := billingCfg(users)
	cfg.KratosClient = kratosclient.NewClient(srv.URL, srv.URL)

	body := `{"ttl_s":120}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/u1/login-token", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["ok"] != true || resp["url"] == "" || resp["expires_in"] != float64(120) {
		t.Fatalf("bad login-token envelope: %v", resp)
	}
	// JAB-232: the minted URL must carry BOTH the flow and the code, so the
	// SPA /recovery page can redeem it (a bare recovery_link lands on /login).
	gotURL, _ := resp["url"].(string)
	if !strings.Contains(gotURL, "flow=f1") || !strings.Contains(gotURL, "code=SECRETCODE") {
		t.Fatalf("login-token url must contain flow + code, got %q", gotURL)
	}
	if probe.calls != 1 {
		t.Fatalf("expected exactly one kratos recovery call, got %d", probe.calls)
	}
}

func TestAutomationLoginToken_AdminRefused(t *testing.T) {
	srv, probe := fakeKratosAdmin(t)
	kid := "11111111-2222-3333-4444-555555555555"
	users := newAbUsers(&models.User{ID: "adm", Email: "root@x", IsAdmin: true, KratosIdentityID: &kid})
	cfg := billingCfg(users)
	cfg.KratosClient = kratosclient.NewClient(srv.URL, srv.URL)

	body := `{}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/adm/login-token", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("admin login link: want 409, got %d", w.Code)
	}
	if probe.calls != 0 {
		t.Fatal("must never reach kratos for an admin target")
	}
}

func TestAutomationLoginToken_NoIdentity409(t *testing.T) {
	srv, _ := fakeKratosAdmin(t)
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un})
	cfg := billingCfg(users)
	cfg.KratosClient = kratosclient.NewClient(srv.URL, srv.URL)

	body := `{}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/u1/login-token", body, billingTok())
	if w.Code != http.StatusConflict {
		t.Fatalf("identity-less user: want 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAutomationLoginToken_TTLClampedOnKratosRequest(t *testing.T) {
	srv, probe := fakeKratosAdmin(t)
	kid := "11111111-2222-3333-4444-555555555555"
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un, KratosIdentityID: &kid})
	cfg := billingCfg(users)
	cfg.KratosClient = kratosclient.NewClient(srv.URL, srv.URL)

	body := `{"ttl_s":999999}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/u1/login-token", body, billingTok())
	resp := decodeBody(t, w)
	if resp["expires_in"] != float64(3600) {
		t.Fatalf("ttl must clamp to 3600 in the response, got %v", resp["expires_in"])
	}
	// The clamp must reach KRATOS, not just the echoed response.
	if probe.lastExpIn != "3600s" {
		t.Fatalf("kratos was asked for expires_in=%q, want 3600s", probe.lastExpIn)
	}
}

func TestAutomationLoginToken_NeverLeaksLinkOrCodeToAudit(t *testing.T) {
	srv, _ := fakeKratosAdmin(t)
	kid := "11111111-2222-3333-4444-555555555555"
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un, KratosIdentityID: &kid})
	cfg := billingCfg(users)
	cfg.KratosClient = kratosclient.NewClient(srv.URL, srv.URL)

	body := `{}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/u1/login-token", body, billingTok())
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	for _, ev := range auditRows(cfg) {
		blob := ev.Action + ev.TargetID + string(ev.Meta)
		if strings.Contains(blob, "SECRETCODE") || strings.Contains(blob, "recovery") {
			t.Fatalf("audit row leaked the recovery link/code: %+v", ev)
		}
	}
}

func TestAutomationLoginToken_KratosErrorNoLeak(t *testing.T) {
	srv, probe := fakeKratosAdmin(t)
	probe.status = http.StatusInternalServerError
	kid := "11111111-2222-3333-4444-555555555555"
	un := "bob"
	users := newAbUsers(&models.User{ID: "u1", Email: "b@x.com", Username: &un, KratosIdentityID: &kid})
	cfg := billingCfg(users)
	cfg.KratosClient = kratosclient.NewClient(srv.URL, srv.URL)

	body := `{}`
	r := abRouterWithBody(cfg, billingTok(), body)
	w := abReq(r, http.MethodPost, "/users/u1/login-token", body, billingTok())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("kratos failure: want 502, got %d: %s", w.Code, w.Body.String())
	}
	// The generic error must not echo the Kratos response body.
	if strings.Contains(w.Body.String(), "kratos boom") {
		t.Fatalf("client response leaked the kratos error body: %s", w.Body.String())
	}
	// Failure is audited (result=error), never the code.
	rows := auditRows(cfg)
	if len(rows) == 0 || rows[len(rows)-1].Result != models.AuditResultError {
		t.Fatalf("expected an error audit row, got %+v", rows)
	}
}

func TestAutomationLoginToken_NotMountedWithoutKratos(t *testing.T) {
	// No KratosClient → route absent and capability not advertised.
	users := newAbUsers()
	cfg := billingCfg(users)

	r := abRouterWithBody(cfg, billingTok(), "{}")
	w := abReq(r, http.MethodPost, "/users/u1/login-token", "{}", billingTok())
	if w.Code != http.StatusNotFound {
		t.Fatalf("route must be absent without Kratos, got %d", w.Code)
	}

	w2 := abReq(r, http.MethodGet, "/capabilities", "", billingTok())
	var body struct {
		Actions []capability `json:"actions"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &body)
	for _, a := range body.Actions {
		if a.Action == "users.login_token" {
			t.Fatal("users.login_token must not be advertised without Kratos")
		}
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
