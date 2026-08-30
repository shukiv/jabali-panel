package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mockPHPPoolIniOverrideRepo is a no-op override store so the set() handler's
// async reconcile goroutine (which lists overrides) has a non-nil dependency.
type mockPHPPoolIniOverrideRepo struct{}

func (mockPHPPoolIniOverrideRepo) Create(context.Context, *models.PHPPoolIniOverride) error {
	return nil
}
func (mockPHPPoolIniOverrideRepo) FindByID(context.Context, string) (*models.PHPPoolIniOverride, error) {
	return nil, repository.ErrNotFound
}
func (mockPHPPoolIniOverrideRepo) ListByPool(context.Context, string) ([]models.PHPPoolIniOverride, error) {
	return nil, nil
}
func (mockPHPPoolIniOverrideRepo) Update(context.Context, *models.PHPPoolIniOverride) error {
	return nil
}
func (mockPHPPoolIniOverrideRepo) Delete(context.Context, string) error { return nil }

// extListJSON is a canned php.ext.list agent reply mixing the four extension
// classes the handler must distinguish.
func extListJSON() json.RawMessage {
	return json.RawMessage(`{"version":"8.4","extensions":[
		{"name":"mbstring","installed":true,"enabled":true,"built_in":true},
		{"name":"redis","installed":true,"enabled":true,"built_in":false},
		{"name":"imagick","installed":true,"enabled":false,"built_in":false},
		{"name":"xdebug","installed":true,"enabled":false,"built_in":false}
	]}`)
}

// setupExtRouter wires the extensions handler with a package that allows FPM
// editing and one (user, version) pool. agentFn drives the mock agent.
func setupExtRouter(
	t *testing.T,
	pool *models.PHPPool,
	agentFn func(ctx context.Context, command string, params any) (json.RawMessage, error),
) (*gin.Engine, *mockPHPPoolRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	pkgID := "pkg1"
	users := &mockUserRepo{users: map[string]*models.User{
		"u1": {ID: "u1", PackageID: &pkgID},
	}}
	packages := &mockPackageRepo{packages: map[string]*models.HostingPackage{
		"pkg1": {ID: "pkg1", FpmUserCanEdit: true},
	}}
	pools := newMockPHPPoolRepo()
	pools.Create(context.Background(), pool)
	RegisterPHPPoolExtensionsRoutes(r.Group("/api/v1"), PHPPoolExtensionsHandlerConfig{
		Agent:               &mockAgent{callFn: agentFn},
		Users:               users,
		Packages:            packages,
		PHPPools:            pools,
		PHPPoolIniOverrides: mockPHPPoolIniOverrideRepo{},
	})
	return r, pools
}

func TestPHPExtensionsList_ReflectsActualState(t *testing.T) {
	pool := &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4",
		ExtraExtensions: models.StringList{"imagick"}, XdebugEnabled: true}
	r, _ := setupExtRouter(t, pool, func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		if cmd == "php.ext.list" {
			return extListJSON(), nil
		}
		return json.RawMessage(`{}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/php-extensions?php_version=8.4", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var got struct {
		Available []string `json:"available"`
		Enabled   []string `json:"enabled"`
		AlwaysOn  []string `json:"always_on"`
		XdebugOn  bool     `json:"xdebug_on"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// available = installed && !built_in (mbstring is built-in → excluded).
	assertSet(t, "available", got.Available, []string{"redis", "imagick", "xdebug"})
	// always_on = server-default-enabled (built-ins included).
	assertSet(t, "always_on", got.AlwaysOn, []string{"mbstring", "redis"})
	// enabled = the pool's opt-in extras, verbatim.
	assertSet(t, "enabled", got.Enabled, []string{"imagick"})
	if !got.XdebugOn {
		t.Fatalf("xdebug_on should be true")
	}
}

func TestPHPExtensionsList_AgentDownOmitsAlwaysOn(t *testing.T) {
	// On an agent hiccup the always_on key must be ABSENT (unknown), not an empty
	// list — otherwise the UI would render every server default as "off".
	pool := &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4"}
	r, _ := setupExtRouter(t, pool, func(context.Context, string, any) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/php-extensions?php_version=8.4", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["always_on"]; present {
		t.Fatalf("always_on must be omitted when the agent is unreachable, body=%s", w.Body.String())
	}
	if _, present := raw["available"]; !present {
		t.Fatalf("available should still be present (static-catalog fallback)")
	}
}

func TestPHPExtensionsList_VersionNotInstalled404(t *testing.T) {
	// An orphaned pool whose PHP version was uninstalled: the agent reports
	// FailedPrecondition, and list() must 404 rather than fall back to the static
	// catalog (which would present togglable checkboxes for a phantom version).
	pool := &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.5"}
	r, _ := setupExtRouter(t, pool, func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		if cmd == "php.ext.list" {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "PHP 8.5 is not installed"}
		}
		return json.RawMessage(`{}`), nil
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/php-extensions?php_version=8.5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 for uninstalled version, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "version_not_installed") {
		t.Fatalf("want version_not_installed, got %s", w.Body.String())
	}
}

func TestPHPExtensionsSet_StripsXdebugAndServerDefaults(t *testing.T) {
	pool := &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4"}
	r, pools := setupExtRouter(t, pool, func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		if cmd == "php.ext.list" {
			return extListJSON(), nil
		}
		return json.RawMessage(`{}`), nil
	})

	body, _ := json.Marshal(map[string]any{
		"php_version": "8.4",
		// imagick: genuine extra (kept). xdebug: own-control (stripped).
		// redis: server default (stripped). bogus: unknown (stripped).
		"extensions": []string{"imagick", "xdebug", "redis", "bogus"},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/php-extensions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	pools.settleReconcile(t)
	pool1, _ := pools.get("p1")
	if got := []string(pool1.ExtraExtensions); !equalSet(got, []string{"imagick"}) {
		t.Fatalf("ExtraExtensions = %v, want [imagick] (xdebug/redis/bogus stripped)", got)
	}
}

func TestPHPExtensionsSet_StripsXdebugEvenWhenAgentDown(t *testing.T) {
	// The xdebug strip is static, so the dual-control guard holds even with no
	// agent (server-default stripping is best-effort and simply doesn't run).
	pool := &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4"}
	r, pools := setupExtRouter(t, pool, func(context.Context, string, any) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	})

	body, _ := json.Marshal(map[string]any{
		"php_version": "8.4",
		"extensions":  []string{"imagick", "xdebug"},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/php-extensions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	pools.settleReconcile(t)
	pool1, _ := pools.get("p1")
	if got := []string(pool1.ExtraExtensions); !equalSet(got, []string{"imagick"}) {
		t.Fatalf("ExtraExtensions = %v, want [imagick] (xdebug stripped statically)", got)
	}
}

func assertSet(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !equalSet(got, want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
