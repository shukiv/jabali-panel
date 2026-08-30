package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mockPHPPoolRepo is a minimal in-memory PHPPoolRepository used by the
// domain↔pool binding tests. It implements only the methods the bind
// handler touches; unused methods return zero values. The mutex guards the
// map: the extensions/tuning handlers fire an async reconcile goroutine
// (reconcilePHPPoolViaAgent) that reads the pools concurrently with the test,
// which the -race detector flags without it.
type mockPHPPoolRepo struct {
	mu    sync.Mutex
	pools map[string]*models.PHPPool
	// updated carries each Update's resulting Status so a test can wait for the
	// async reconcile goroutine's terminal Update ("ready"/"error") before it
	// reads the pool — the goroutine mutates pool.Status directly, so reading
	// mid-flight is a -race.
	updated chan string
}

func newMockPHPPoolRepo() *mockPHPPoolRepo {
	return &mockPHPPoolRepo{
		pools:   make(map[string]*models.PHPPool),
		updated: make(chan string, 16),
	}
}

// settleReconcile blocks until the async reconcile goroutine posts its terminal
// (non-"pending") Update, so the caller can read the pool without racing the
// goroutine's pool.Status write.
func (m *mockPHPPoolRepo) settleReconcile(t *testing.T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case st := <-m.updated:
			if st != "pending" {
				return
			}
		case <-deadline:
			t.Fatal("reconcile did not settle in time")
		}
	}
}

func (m *mockPHPPoolRepo) Create(ctx context.Context, p *models.PHPPool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pools[p.ID] = p
	return nil
}

func (m *mockPHPPoolRepo) FindByID(ctx context.Context, id string) (*models.PHPPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockPHPPoolRepo) FindByUserID(ctx context.Context, userID string) (*models.PHPPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pools {
		if p.UserID == userID {
			return p, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockPHPPoolRepo) FindByUserAndVersion(ctx context.Context, userID, phpVersion string) (*models.PHPPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pools {
		if p.UserID == userID && p.PHPVersion == phpVersion {
			return p, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockPHPPoolRepo) ListByUserID(ctx context.Context, userID string) ([]models.PHPPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []models.PHPPool
	for _, p := range m.pools {
		if p.UserID == userID {
			result = append(result, *p)
		}
	}
	return result, nil
}

func (m *mockPHPPoolRepo) ListAll(ctx context.Context, opts repository.ListOptions) ([]models.PHPPool, int64, error) {
	return nil, 0, nil
}

func (m *mockPHPPoolRepo) Update(ctx context.Context, p *models.PHPPool) error {
	m.mu.Lock()
	m.pools[p.ID] = p
	st := p.Status
	m.mu.Unlock()
	select {
	case m.updated <- st:
	default:
	}
	return nil
}

func (m *mockPHPPoolRepo) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pools, id)
	return nil
}

func (m *mockPHPPoolRepo) SetStatus(ctx context.Context, id, status string, lastErr *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[id]; ok {
		p.Status = status
	}
	return nil
}

// get returns a value copy of a pool under the lock, for test assertions that
// would otherwise read the map unguarded while the async reconcile goroutine is
// still touching it (-race).
func (m *mockPHPPoolRepo) get(id string) (models.PHPPool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[id]; ok {
		return *p, true
	}
	return models.PHPPool{}, false
}

func setupBindRouter(t *testing.T, claims auth.AccessClaims) (*gin.Engine, *mockDomainRepo, *mockPHPPoolRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Inject claims into the gin context as the real auth middleware would.
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &claims)
		c.Next()
	})
	domains := newMockDomainRepo()
	pools := newMockPHPPoolRepo()
	RegisterDomainPHPPoolRoutes(r.Group("/api/v1"), DomainPHPPoolHandlerConfig{
		Domains:  domains,
		PHPPools: pools,
	})
	return r, domains, pools
}

func TestBindDomainPHPPool_ByPoolID(t *testing.T) {
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1", IsAdmin: false})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "example.com"})
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.3"})

	body, _ := json.Marshal(map[string]string{"pool_id": "p1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := domains.domains["d1"].PHPPoolID; got == nil || *got != "p1" {
		t.Fatalf("domain not bound to pool: %+v", got)
	}
}

func TestBindDomainPHPPool_ByPHPVersion_Match(t *testing.T) {
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "example.com"})
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.3"})

	body, _ := json.Marshal(map[string]string{"php_version": "8.3"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := domains.domains["d1"].PHPPoolID; got == nil || *got != "p1" {
		t.Fatalf("domain not bound: %+v", got)
	}
}

func TestBindDomainPHPPool_ByPHPVersion_NonDefault_CreatesVersionedPool(t *testing.T) {
	// GH #329: a user-driven version switch to a NON-default version creates a
	// separate (user, version) pool and binds THIS domain to it — leaving the
	// default pool (and every sibling domain) untouched.
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "example.com"})
	// Default pool (earliest) at 8.3.
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.3", Status: "active"})

	body, _ := json.Marshal(map[string]string{"php_version": "8.1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	// The default pool must be untouched.
	if p, _ := pools.get("p1"); p.PHPVersion != "8.3" {
		t.Fatalf("default pool version must not change, got %q", p.PHPVersion)
	}
	// A new 8.1 pool must exist (pending) and the domain bound to it.
	newPool, err := pools.FindByUserAndVersion(context.Background(), "u1", "8.1")
	if err != nil {
		t.Fatalf("versioned 8.1 pool not created: %v", err)
	}
	if newPool.Status != "pending" {
		t.Fatalf("new versioned pool should be pending, got %q", newPool.Status)
	}
	bound := domains.domains["d1"].PHPPoolID
	if bound == nil || *bound != newPool.ID {
		t.Fatalf("domain should be bound to the new versioned pool %q, got %+v", newPool.ID, bound)
	}
	if *bound == "p1" {
		t.Fatalf("domain must not stay on the default pool")
	}
}

func TestBindDomainPHPPool_ByPHPVersion_Default_BindsDefaultPool(t *testing.T) {
	// GH #329: requesting the user's DEFAULT version binds to the existing
	// default pool — no new pool is created.
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "example.com"})
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.3", Status: "active"})

	body, _ := json.Marshal(map[string]string{"php_version": "8.3"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := domains.domains["d1"].PHPPoolID; got == nil || *got != "p1" {
		t.Fatalf("domain should bind to the default pool p1, got %+v", got)
	}
	if len(pools.pools) != 1 {
		t.Fatalf("no new pool should be created for the default version, have %d", len(pools.pools))
	}
}

func TestBindDomainPHPPool_ByPHPVersion_InvalidFormat(t *testing.T) {
	// GH #329: the version format guard is the injection defense (the value
	// becomes an FPM slug + systemd instance downstream).
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "example.com"})
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.3", Status: "active"})

	body, _ := json.Marshal(map[string]string{"php_version": "8.1/../etc"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for malformed version, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBindDomainPHPPool_MissingBothFields(t *testing.T) {
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1"})
	_ = pools

	body, _ := json.Marshal(map[string]string{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBindDomainPHPPool_BothFields(t *testing.T) {
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1"})
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.3"})

	body, _ := json.Marshal(map[string]string{"pool_id": "p1", "php_version": "8.3"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/d1/php-pool", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when both fields set, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnbindDomainPHPPool(t *testing.T) {
	r, domains, pools := setupBindRouter(t, auth.AccessClaims{UserID: "u1"})
	poolID := "p1"
	domains.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", PHPPoolID: &poolID})
	pools.Create(context.Background(), &models.PHPPool{ID: poolID, UserID: "u1", PHPVersion: "8.3"})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/domains/d1/php-pool", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if domains.domains["d1"].PHPPoolID != nil {
		t.Fatalf("domain should be unbound")
	}
}
