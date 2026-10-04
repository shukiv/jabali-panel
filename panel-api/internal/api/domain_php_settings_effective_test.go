package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// effectiveAgent answers php.pool.effective and records what it was asked.
type effectiveAgent struct {
	mu     sync.Mutex
	calls  int
	params map[string]any
}

func (a *effectiveAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cmd != "php.pool.effective" {
		return json.RawMessage(`{}`), nil
	}
	a.calls++
	a.params, _ = params.(map[string]any)
	return json.RawMessage(`{"php_version":"8.4","slug":"u1-php8.4","pool_found":true,
		"disabled_functions":[{"name":"exec","source":"pool"}],
		"php_defense":{"active":true,"mode":"enforce","pool_rules":false,"functions":[{"name":"shell_exec","state":"blocked"}]},
		"include_path":{"value":".:/usr/share/php","source":"php.ini"},
		"session_save_path":{"value":"","source":"php.ini"}}`), nil
}

func newEffectiveRouter(t *testing.T, callerID string, isAdmin bool, ag *effectiveAgent) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	phpEffectiveMu.Lock()
	phpEffectiveCache = map[string]cachedPHPEffective{}
	phpEffectiveMu.Unlock()

	domains := newMockDomainRepo()
	poolID := "pool-84"
	domains.domains["d1"] = &models.Domain{ID: "d1", UserID: "u1", Name: "ex.test", PHPPoolID: &poolID}
	pools := &orderedPoolRepo{mockPHPPoolRepo: newMockPHPPoolRepo(), order: []string{"pool-83", "pool-84"}}
	// u1's default pool runs 8.3; the domain is bound to a second, 8.4 pool,
	// so its slug is the versioned one.
	pools.pools["pool-83"] = &models.PHPPool{ID: "pool-83", UserID: "u1", PHPVersion: "8.3"}
	pools.pools[poolID] = &models.PHPPool{ID: poolID, UserID: "u1", PHPVersion: "8.4"}
	users := &mockUserRepo{users: map[string]*models.User{"u1": {ID: "u1", Username: strp("u1")}}}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: callerID, IsAdmin: isAdmin})
		c.Next()
	})
	RegisterDomainPHPSettingsRoutes(r.Group("/api/v1"), DomainPHPSettingsHandlerConfig{
		Domains:  domains,
		PHPPools: pools,
		Users:    users,
		Agent:    ag,
	})
	return r
}

func getEffective(r *gin.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/domains/d1/php-settings/effective", nil))
	return w
}

func TestPHPSettingsEffective_OwnerGetsThePoolsRealConfig(t *testing.T) {
	ag := &effectiveAgent{}
	w := getEffective(newEffectiveRouter(t, "u1", false, ag))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if ag.params["php_version"] != "8.4" || ag.params["slug"] != "u1-php8.4" {
		t.Fatalf("agent asked about %v, want the bound 8.4 pool's versioned slug", ag.params)
	}
	var body domainPHPEffectiveResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.DisabledFunctions) != 1 || body.DisabledFunctions[0].Source != "pool" ||
		body.PHPDefense.Mode != "enforce" || body.PHPDefense.Functions[0].State != "blocked" ||
		body.IncludePath.Value != ".:/usr/share/php" {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestPHPSettingsEffective_OtherTenantRefused(t *testing.T) {
	ag := &effectiveAgent{}
	w := getEffective(newEffectiveRouter(t, "u2", false, ag))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	if ag.calls != 0 {
		t.Fatal("the agent was asked about another tenant's pool")
	}
}

func TestPHPSettingsEffective_AdminAllowedAndCached(t *testing.T) {
	ag := &effectiveAgent{}
	r := newEffectiveRouter(t, "admin1", true, ag)
	for i := 0; i < 3; i++ {
		if w := getEffective(r); w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", w.Code)
		}
	}
	if ag.calls != 1 {
		t.Fatalf("agent called %d times; repeated page loads must hit the cache", ag.calls)
	}
}
