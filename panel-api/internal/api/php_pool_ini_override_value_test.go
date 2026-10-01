package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func setupIniOverrideRouter(t *testing.T) (*gin.Engine, *statefulOverrideRepo, *mockPHPPoolRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	})
	uname := "alice"
	pools := newMockPHPPoolRepo()
	pools.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4"})
	ov := newStatefulOverrideRepo()
	ov.Create(context.Background(), &models.PHPPoolIniOverride{ID: "o1", PoolID: "p1", Directive: "memory_limit", Value: "256M", Kind: "value"})
	ov.Create(context.Background(), &models.PHPPoolIniOverride{ID: "o2", PoolID: "p1", Directive: "file_uploads", Value: "on", Kind: "flag"})
	RegisterPHPPoolRoutes(r.Group("/api/v1"), PHPPoolHandlerConfig{
		PHPPools:            pools,
		PHPPoolIniOverrides: ov,
		Users:               &mockUserRepo{users: map[string]*models.User{"u1": {ID: "u1", Username: &uname}}},
		Packages:            &mockPackageRepo{packages: map[string]*models.HostingPackage{}},
		Agent: &mockAgent{callFn: func(context.Context, string, any) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}},
	})
	return r, ov, pools
}

func iniOverrideReq(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// A pool override value is written raw into the pool conf. A newline would add
// a line past the agent's directive allowlist, so the API refuses control
// characters on create and on update, and stores nothing.
func TestIniOverride_RejectsControlCharacters(t *testing.T) {
	r, ov, _ := setupIniOverrideRouter(t)
	for _, v := range []string{"256M\nphp_admin_value[open_basedir] = /", "256M\r", "256M\x00"} {
		body, _ := json.Marshal(map[string]string{"directive": "memory_limit", "value": v, "kind": "value"})
		w := iniOverrideReq(t, r, http.MethodPost, "/api/v1/php-pools/p1/ini-overrides", string(body))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_value") {
			t.Fatalf("create %q: %d %s, want 400 invalid_value", v, w.Code, w.Body.String())
		}
		body, _ = json.Marshal(map[string]string{"value": v})
		w = iniOverrideReq(t, r, http.MethodPut, "/api/v1/php-pools/p1/ini-overrides/o1", string(body))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_value") {
			t.Fatalf("update %q: %d %s, want 400 invalid_value", v, w.Code, w.Body.String())
		}
	}
	if len(ov.rows) != 2 || ov.rows["o1"].Value != "256M" {
		t.Fatalf("rows changed: %d rows, o1=%q", len(ov.rows), ov.rows["o1"].Value)
	}
}

// The agent accepts only "on" or "off" for a flag and fails the whole pool
// apply otherwise, so the API refuses anything else and stores "On" as "on".
func TestIniOverride_FlagValueIsOnOrOff(t *testing.T) {
	r, ov, pools := setupIniOverrideRouter(t)
	w := iniOverrideReq(t, r, http.MethodPut, "/api/v1/php-pools/p1/ini-overrides/o2", `{"value":"1"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_value") {
		t.Fatalf("flag 1: %d %s, want 400 invalid_value", w.Code, w.Body.String())
	}
	w = iniOverrideReq(t, r, http.MethodPost, "/api/v1/php-pools/p1/ini-overrides", `{"directive":"log_errors","value":"yes","kind":"flag"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("flag yes: %d %s, want 400", w.Code, w.Body.String())
	}
	w = iniOverrideReq(t, r, http.MethodPut, "/api/v1/php-pools/p1/ini-overrides/o2", `{"value":" Off "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("flag Off: %d %s, want 200", w.Code, w.Body.String())
	}
	pools.settleReconcile(t)
	if got := ov.rows["o2"].Value; got != "off" {
		t.Fatalf("stored flag = %q, want off", got)
	}
}

// An over-long value is a 400, not a database error.
func TestIniOverride_RejectsOverlongValue(t *testing.T) {
	r, _, _ := setupIniOverrideRouter(t)
	body, _ := json.Marshal(map[string]string{"value": strings.Repeat("x", 256)})
	w := iniOverrideReq(t, r, http.MethodPut, "/api/v1/php-pools/p1/ini-overrides/o1", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("256 chars: %d %s, want 400", w.Code, w.Body.String())
	}
}
