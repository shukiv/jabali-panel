package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

type icFetcher struct {
	data []byte
	err  error
}

func (f icFetcher) FetchLoader(_ context.Context, _ string) ([]byte, error) { return f.data, f.err }

func icExtRouter(mock agent.AgentInterface, f LoaderFetcher) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
		c.Next()
	})
	RegisterPHPExtensionAdminRoutes(v1, mock, f)
	return r
}

// TestExtList_ComposesIonCubeRow: the ext list gains an "ioncube" row reflecting
// live status for an ionCube-supported version.
func TestExtList_ComposesIonCubeRow(t *testing.T) {
	mock := agent.NewMockClient().
		On("php.ext.list", map[string]any{"version": "8.4", "extensions": []any{
			map[string]any{"name": "curl", "installed": true, "enabled": true, "built_in": false},
		}}).
		On("php.ioncube.status", map[string]any{"installed_versions": []any{"8.4"}, "enabled_versions": []any{"8.4"}})
	r := icExtRouter(mock, icFetcher{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/versions/8.4/extensions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Extensions []struct {
			Name      string `json:"name"`
			Installed bool   `json:"installed"`
			Enabled   bool   `json:"enabled"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range body.Extensions {
		if e.Name == "ioncube" {
			found = true
			if !e.Installed || !e.Enabled {
				t.Errorf("ioncube row installed=%v enabled=%v want true/true", e.Installed, e.Enabled)
			}
		}
	}
	if !found {
		t.Error("no ioncube row composed into ext list")
	}
}

// TestExtApply_IonCubeInstall_FetchesAndInstalls: install action routes through
// the fetcher + php.ioncube.install.
func TestExtApply_IonCubeInstall_FetchesAndInstalls(t *testing.T) {
	mock := agent.NewMockClient().On("php.ioncube.install", map[string]any{"version": "8.4", "installed": true})
	r := icExtRouter(mock, icFetcher{data: []byte("\x7fELF loader")})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.4/extensions/ioncube/apply",
		strings.NewReader(`{"action":"install"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("install code = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestExtApply_IonCubeEnable_ServerSet: enable routes to php.ioncube.server_set.
func TestExtApply_IonCubeEnable_ServerSet(t *testing.T) {
	mock := agent.NewMockClient().On("php.ioncube.server_set", map[string]any{"version": "8.4", "enabled": true})
	r := icExtRouter(mock, icFetcher{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.4/extensions/ioncube/apply",
		strings.NewReader(`{"action":"enable"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable code = %d", rec.Code)
	}
}

// TestExtApply_IonCubeRemove_Blocked409: uninstall refused (server-wide enabled)
// surfaces as 409.
func TestExtApply_IonCubeRemove_Blocked409(t *testing.T) {
	mock := agent.NewMockClient().OnError("php.ioncube.uninstall", &agent.AgentError{
		Code:    agent.CodeFailedPrecondition,
		Message: "still enabled",
	})
	r := icExtRouter(mock, icFetcher{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.4/extensions/ioncube/apply",
		strings.NewReader(`{"action":"remove"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("remove-blocked code = %d want 409", rec.Code)
	}
}

// TestExtApply_IonCubeInstall_FetchFailure_502.
func TestExtApply_IonCubeInstall_FetchFailure_502(t *testing.T) {
	r := icExtRouter(agent.NewMockClient(), icFetcher{err: errors.New("net down")})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.4/extensions/ioncube/apply",
		strings.NewReader(`{"action":"install"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("fetch-fail code = %d want 502", rec.Code)
	}
}
