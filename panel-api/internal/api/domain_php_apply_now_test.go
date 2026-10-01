package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1701: a domain's PHP Settings resets its OPcache, and a save or a version
// switch applies now instead of at the next full reconcile pass.

// orderedPoolRepo keeps pools in creation order, as the real repository's
// created_at ordering does: the earliest is the user's default pool.
type orderedPoolRepo struct {
	*mockPHPPoolRepo
	mu    sync.Mutex
	order []string
}

func (r *orderedPoolRepo) Create(ctx context.Context, p *models.PHPPool) error {
	if err := r.mockPHPPoolRepo.Create(ctx, p); err != nil {
		return err
	}
	r.mu.Lock()
	r.order = append(r.order, p.ID)
	r.mu.Unlock()
	return nil
}

func (r *orderedPoolRepo) ListByUserID(_ context.Context, userID string) ([]models.PHPPool, error) {
	r.mu.Lock()
	ids := append([]string(nil), r.order...)
	r.mu.Unlock()
	var out []models.PHPPool
	for _, id := range ids {
		if p, ok := r.get(id); ok && p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

// applyAgent records agent calls; it is called from the version switch's
// goroutine, so it locks.
type applyAgent struct {
	mu    sync.Mutex
	calls []applyAgentCall
	fail  error
}

type applyAgentCall struct {
	cmd    string
	params map[string]any
}

func (a *applyAgent) Call(_ context.Context, cmd string, p any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m, _ := p.(map[string]any)
	a.calls = append(a.calls, applyAgentCall{cmd: cmd, params: m})
	if a.fail != nil {
		return nil, a.fail
	}
	return json.RawMessage(`{}`), nil
}

func (a *applyAgent) recorded() []applyAgentCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]applyAgentCall(nil), a.calls...)
}

// chanScheduler hands each scheduled domain to the test, along with whether
// php.pool.apply had already reached the agent at that moment.
type chanScheduler struct {
	ch    chan string
	agent *applyAgent
	mu    sync.Mutex
	early bool // scheduled before any php.pool.apply
}

func newChanScheduler(ag *applyAgent) *chanScheduler {
	return &chanScheduler{ch: make(chan string, 8), agent: ag}
}

func (s *chanScheduler) Schedule(id string) {
	applied := false
	for _, c := range s.agent.recorded() {
		if c.cmd == "php.pool.apply" {
			applied = true
		}
	}
	s.mu.Lock()
	if !applied {
		s.early = true
	}
	s.mu.Unlock()
	s.ch <- id
}

func (s *chanScheduler) next(t *testing.T) string {
	t.Helper()
	select {
	case id := <-s.ch:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("domain was never scheduled")
		return ""
	}
}

func (s *chanScheduler) none(t *testing.T) {
	t.Helper()
	select {
	case id := <-s.ch:
		t.Fatalf("domain %q scheduled, want none", id)
	case <-time.After(150 * time.Millisecond):
	}
}

type applyNowFixture struct {
	router  *gin.Engine
	domains *phpSettingsWriteRepo
	pools   *orderedPoolRepo
	pkg     *models.HostingPackage
	agent   *applyAgent
	sched   *chanScheduler
	claims  *auth.AccessClaims
}

// newApplyNowFixture: tenant u1 ("alice", package pkg1 with FPM editing on)
// owns domain d1; their default pool p1 runs PHP 8.4. The caller is u1.
func newApplyNowFixture(t *testing.T) *applyNowFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &applyNowFixture{
		claims: &auth.AccessClaims{UserID: "u1"},
		pkg:    &models.HostingPackage{ID: "pkg1", FpmUserCanEdit: true},
		agent:  &applyAgent{},
	}
	f.sched = newChanScheduler(f.agent)
	base := newMockDomainRepo()
	base.domains["d1"] = &models.Domain{ID: "d1", UserID: "u1", Name: "ex.test"}
	base.domains["d2"] = &models.Domain{ID: "d2", UserID: "u2", Name: "other.test"}
	f.domains = &phpSettingsWriteRepo{mockDomainRepo: base}
	f.pools = &orderedPoolRepo{mockPHPPoolRepo: newMockPHPPoolRepo()}
	_ = f.pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4", Status: "active"})
	pkgID := "pkg1"
	alice, bob := "alice", "bob"
	users := &mockUserRepo{users: map[string]*models.User{
		"u1": {ID: "u1", Username: &alice, PackageID: &pkgID},
		"u2": {ID: "u2", Username: &bob},
	}}
	packages := &mockPackageRepo{packages: map[string]*models.HostingPackage{"pkg1": f.pkg}}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		claims := *f.claims
		ginctx.SetClaims(c, &claims)
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterDomainPHPSettingsRoutes(v1, DomainPHPSettingsHandlerConfig{
		Domains:    f.domains,
		PHPPools:   f.pools,
		Agent:      f.agent,
		Users:      users,
		Packages:   packages,
		Reconciler: f.sched,
	})
	RegisterDomainPHPPoolRoutes(v1, DomainPHPPoolHandlerConfig{
		Domains:             f.domains,
		PHPPools:            f.pools,
		PHPPoolIniOverrides: &mockPHPPoolIniOverrideRepo{},
		Users:               users,
		Agent:               f.agent,
		Packages:            packages,
		Reconciler:          f.sched,
	})
	f.router = r
	return f
}

func (f *applyNowFixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// opcacheResets returns the slugs php.opcache.reset was called with.
func (f *applyNowFixture) opcacheResets() []string {
	var out []string
	for _, c := range f.agent.recorded() {
		if c.cmd == "php.opcache.reset" {
			out = append(out, c.params["username"].(string)+"/"+c.params["slug"].(string))
		}
	}
	return out
}

func (f *applyNowFixture) resetAllowed(t *testing.T) bool {
	t.Helper()
	w := f.do(t, http.MethodGet, "/api/v1/domains/d1/php-settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET php-settings: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Allowed *bool `json:"opcache_reset_allowed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Allowed == nil {
		t.Fatalf("GET php-settings has no opcache_reset_allowed: %s", w.Body.String())
	}
	return *resp.Allowed
}

func TestDomainOpcacheReset_RestartsTheDefaultPoolOfAnUnboundDomain(t *testing.T) {
	f := newApplyNowFixture(t)
	if !f.resetAllowed(t) {
		t.Error("opcache_reset_allowed = false for a tenant whose package lets them edit FPM")
	}
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-settings/opcache-reset", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if got := f.opcacheResets(); len(got) != 1 || got[0] != "alice/alice" {
		t.Errorf("php.opcache.reset calls = %v, want [alice/alice]", got)
	}
}

func TestDomainOpcacheReset_RestartsTheVersionedPoolTheDomainIsBoundTo(t *testing.T) {
	f := newApplyNowFixture(t)
	_ = f.pools.Create(context.Background(), &models.PHPPool{ID: "p2", UserID: "u1", PHPVersion: "8.3", Status: "ready"})
	p2 := "p2"
	f.domains.domains["d1"].PHPPoolID = &p2
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-settings/opcache-reset", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if got := f.opcacheResets(); len(got) != 1 || got[0] != "alice/alice-php8.3" {
		t.Errorf("php.opcache.reset calls = %v, want [alice/alice-php8.3]", got)
	}
}

func TestDomainOpcacheReset_TenantWithoutFPMEditingIsRefused(t *testing.T) {
	f := newApplyNowFixture(t)
	f.pkg.FpmUserCanEdit = false
	if f.resetAllowed(t) {
		t.Error("opcache_reset_allowed = true without FPM editing")
	}
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-settings/opcache-reset", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("reset: %d %s, want 403", w.Code, w.Body.String())
	}
	if got := f.opcacheResets(); len(got) != 0 {
		t.Errorf("php.opcache.reset called %v on a refused request", got)
	}
}

func TestDomainOpcacheReset_AnotherUsersDomainIsRefused(t *testing.T) {
	f := newApplyNowFixture(t)
	w := f.do(t, http.MethodPost, "/api/v1/domains/d2/php-settings/opcache-reset", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("reset of another user's domain: %d %s, want 403", w.Code, w.Body.String())
	}
	if got := f.opcacheResets(); len(got) != 0 {
		t.Errorf("php.opcache.reset called %v", got)
	}
}

func TestDomainOpcacheReset_AdminMayResetEvenWhenTheOwnersPackageMayNot(t *testing.T) {
	for name, claims := range map[string]auth.AccessClaims{
		"admin":                {UserID: "admin1", IsAdmin: true},
		"admin acting as user": {UserID: "u1", ImpersonatedBy: "admin1"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newApplyNowFixture(t)
			f.pkg.FpmUserCanEdit = false
			*f.claims = claims
			if !f.resetAllowed(t) {
				t.Error("opcache_reset_allowed = false for an admin")
			}
			w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-settings/opcache-reset", nil)
			if w.Code != http.StatusOK {
				t.Fatalf("reset: %d %s", w.Code, w.Body.String())
			}
			if got := f.opcacheResets(); len(got) != 1 || got[0] != "alice/alice" {
				t.Errorf("php.opcache.reset calls = %v, want [alice/alice] (the owner's pool)", got)
			}
		})
	}
}

func TestDomainOpcacheReset_NoPool(t *testing.T) {
	f := newApplyNowFixture(t)
	_ = f.pools.Delete(context.Background(), "p1")
	if f.resetAllowed(t) {
		t.Error("opcache_reset_allowed = true with no pool to reset")
	}
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-settings/opcache-reset", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("reset without a pool: %d %s, want 404", w.Code, w.Body.String())
	}
}

func TestDomainPHPSettingsSave_SchedulesTheDomain(t *testing.T) {
	f := newApplyNowFixture(t)
	w := f.do(t, http.MethodPatch, "/api/v1/domains/d1/php-settings", map[string]any{"php_memory_limit": "512M"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	if id := f.sched.next(t); id != "d1" {
		t.Errorf("scheduled %q, want d1", id)
	}
}

func TestDomainPHPSettingsSave_RefusedSaveSchedulesNothing(t *testing.T) {
	f := newApplyNowFixture(t)
	w := f.do(t, http.MethodPatch, "/api/v1/domains/d1/php-settings", map[string]any{"php_memory_limit": "lots"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH: %d %s, want 400", w.Code, w.Body.String())
	}
	f.sched.none(t)
}

// A version change marks the owner's pool pending; re-pointing the vhost at it
// before the full pass applies it would send the domain to a dead socket.
func TestDomainPHPSettingsSave_VersionChangeSchedulesNothing(t *testing.T) {
	f := newApplyNowFixture(t)
	w := f.do(t, http.MethodPatch, "/api/v1/domains/d1/php-settings", map[string]any{"php_version": "8.3"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	if p, _ := f.pools.get("p1"); p.Status != "pending" || p.PHPVersion != "8.3" {
		t.Fatalf("pool p1 = %s/%s, want pending/8.3", p.Status, p.PHPVersion)
	}
	f.sched.none(t)
}

func TestDomainPHPSettingsSave_PoolNotRunningSchedulesNothing(t *testing.T) {
	for _, status := range []string{"pending", "error"} {
		t.Run(status, func(t *testing.T) {
			f := newApplyNowFixture(t)
			_ = f.pools.SetStatus(context.Background(), "p1", status, nil)
			w := f.do(t, http.MethodPatch, "/api/v1/domains/d1/php-settings", map[string]any{"php_memory_limit": "512M"})
			if w.Code != http.StatusOK {
				t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
			}
			f.sched.none(t)
		})
	}
}

func TestDomainPHPVersionSwitch_RunningPoolSchedulesTheDomainNow(t *testing.T) {
	f := newApplyNowFixture(t)
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-pool", map[string]any{"php_version": "8.4"})
	if w.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", w.Code, w.Body.String())
	}
	if id := f.sched.next(t); id != "d1" {
		t.Errorf("scheduled %q, want d1", id)
	}
	for _, c := range f.agent.recorded() {
		if c.cmd == "php.pool.apply" {
			t.Error("php.pool.apply called for a pool that already runs")
		}
	}
}

func TestDomainPHPVersionSwitch_NewPoolIsAppliedBeforeTheDomainIsScheduled(t *testing.T) {
	f := newApplyNowFixture(t)
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-pool", map[string]any{"php_version": "8.3"})
	if w.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", w.Code, w.Body.String())
	}
	if id := f.sched.next(t); id != "d1" {
		t.Errorf("scheduled %q, want d1", id)
	}
	f.sched.mu.Lock()
	early := f.sched.early
	f.sched.mu.Unlock()
	if early {
		t.Error("the domain was scheduled before its new pool was applied: its vhost would point at a socket nothing listens on")
	}
	var slug any
	for _, c := range f.agent.recorded() {
		if c.cmd == "php.pool.apply" {
			slug = c.params["slug"]
		}
	}
	if slug != "alice-php8.3" {
		t.Errorf("php.pool.apply slug = %v, want alice-php8.3", slug)
	}
}

func TestDomainPHPVersionSwitch_FailedApplySchedulesNothing(t *testing.T) {
	f := newApplyNowFixture(t)
	f.agent.fail = errors.New("fpm failed to start")
	w := f.do(t, http.MethodPost, "/api/v1/domains/d1/php-pool", map[string]any{"php_version": "8.3"})
	if w.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", w.Code, w.Body.String())
	}
	f.pools.settleReconcile(t)
	f.sched.none(t)
}

func TestDomainPHPVersionUnbind_SchedulesTheDomainOnItsDefaultPool(t *testing.T) {
	f := newApplyNowFixture(t)
	_ = f.pools.Create(context.Background(), &models.PHPPool{ID: "p2", UserID: "u1", PHPVersion: "8.3", Status: "ready"})
	p2 := "p2"
	f.domains.domains["d1"].PHPPoolID = &p2
	w := f.do(t, http.MethodDelete, "/api/v1/domains/d1/php-pool", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("unbind: %d %s", w.Code, w.Body.String())
	}
	if id := f.sched.next(t); id != "d1" {
		t.Errorf("scheduled %q, want d1", id)
	}
}
