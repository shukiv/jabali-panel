package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
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
	// reply overrides the default answer (which predates
	// unavailable_functions, like an agent that does not send it).
	reply string
}

func (a *effectiveAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cmd != "php.pool.effective" {
		return json.RawMessage(`{}`), nil
	}
	a.calls++
	a.params, _ = params.(map[string]any)
	if a.reply != "" {
		return json.RawMessage(a.reply), nil
	}
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
	// An agent reply without unavailable_functions still gives the SPA a list.
	if !strings.Contains(w.Body.String(), `"unavailable_functions":[]`) {
		t.Fatalf("unavailable_functions must be [] when the agent sends none: %s", w.Body.String())
	}
}

// GH #1701: the functions PHP-FPM does not provide reach the SPA.
func TestPHPSettingsEffective_PassesUnavailableFunctions(t *testing.T) {
	ag := &effectiveAgent{reply: `{"php_version":"8.4","slug":"u1-php8.4","pool_found":true,
		"disabled_functions":[],"php_defense":{"active":false,"mode":"","pool_rules":false,"functions":[]},
		"include_path":{"value":"","source":"php.ini"},"session_save_path":{"value":"","source":"php.ini"},
		"unavailable_functions":["dl","pcntl_exec"],"availability_error":""}`}
	w := getEffective(newEffectiveRouter(t, "u1", false, ag))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var body domainPHPEffectiveResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.UnavailableFunctions) != 2 || body.UnavailableFunctions[0] != "dl" || body.UnavailableFunctions[1] != "pcntl_exec" {
		t.Fatalf("unavailable = %v", body.UnavailableFunctions)
	}
}

// GH #2001: an enforced PHP-FPM AppArmor profile reaches the SPA, so it can
// say exec and friends start only the shell and cat.
func TestPHPSettingsEffective_PassesExecConfined(t *testing.T) {
	ag := &effectiveAgent{reply: `{"php_version":"8.4","slug":"u1-php8.4","pool_found":true,
		"disabled_functions":[],"php_defense":{"active":false,"mode":"","pool_rules":false,"functions":[]},
		"include_path":{"value":"","source":"php.ini"},"session_save_path":{"value":"","source":"php.ini"},
		"unavailable_functions":[],"exec_confined":true}`}
	w := getEffective(newEffectiveRouter(t, "u1", false, ag))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"exec_confined":true`) {
		t.Fatalf("want 200 with exec_confined true, got %d: %s", w.Code, w.Body.String())
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

// The API decodes the agent's php.pool.effective reply into a typed struct, so
// a field the agent adds is silently dropped unless the struct has it too
// (GH #1701 unavailable_functions). Every agent JSON field but the slug (the
// API knows it already) must exist here.
func TestPHPSettingsEffective_MirrorsAgentResponse(t *testing.T) {
	src, err := os.ReadFile("../../../panel-agent/internal/commands/php_pool_effective.go")
	if err != nil {
		t.Skipf("agent source not readable (%v) — skipping cross-boundary check", err)
	}
	body := regexp.MustCompile(`(?s)type phpPoolEffectiveResponse struct \{(.*?)\n\}`).FindSubmatch(src)
	if body == nil {
		t.Fatal("agent phpPoolEffectiveResponse not found")
	}
	ours := map[string]bool{}
	rt := reflect.TypeOf(domainPHPEffectiveResponse{})
	for i := 0; i < rt.NumField(); i++ {
		ours[strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	tags := regexp.MustCompile("json:\"([a-z_]+)").FindAllSubmatch(body[1], -1)
	if len(tags) == 0 {
		t.Fatal("no json tags found in the agent struct")
	}
	for _, m := range tags {
		if name := string(m[1]); name != "slug" && !ours[name] {
			t.Errorf("agent field %q is dropped by domainPHPEffectiveResponse", name)
		}
	}
}
