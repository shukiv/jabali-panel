package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

func injectAdminClaims(isAdmin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: isAdmin})
		c.Next()
	}
}

func newAdminUpdatesRouter(mock *agent.MockClient, isAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(isAdmin))
	api.RegisterAdminUpdatesRoutes(v1, api.AdminUpdatesHandlerConfig{Agent: mock})
	return r
}

func TestAdminUpdates_RBAC_RejectsNonAdmin(t *testing.T) {
	mock := agent.NewMockClient()
	r := newAdminUpdatesRouter(mock, false)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/updates/jabali/check", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAdminUpdates_JabaliCheck_HappyPath(t *testing.T) {
	mock := agent.NewMockClient().On("system.update_check", map[string]any{
		"current_sha":  "abc123",
		"remote_sha":   "def456",
		"behind_count": 2,
		"branch":       "main",
	})
	r := newAdminUpdatesRouter(mock, true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/updates/jabali/check", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"behind_count":2`)
}

func TestAdminUpdates_JabaliRun_ReturnsUnit(t *testing.T) {
	mock := agent.NewMockClient().On("system.update_run", map[string]any{
		"unit":       "jabali-update-oneshot.service",
		"started_at": "2026-04-25T10:00:00Z",
	})
	r := newAdminUpdatesRouter(mock, true)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/updates/jabali/run", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"unit":"jabali-update-oneshot.service"`)
}

func TestAdminUpdates_AptCheck_HappyPath(t *testing.T) {
	mock := agent.NewMockClient().On("system.apt_check", map[string]any{
		"packages": []map[string]any{
			{"name": "curl", "current": "8.4.0-2", "new": "8.5.0-2", "source": "stable"},
		},
		"total": 1,
	})
	r := newAdminUpdatesRouter(mock, true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/updates/apt/check", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"name":"curl"`)
}

func TestAdminUpdates_Status_PassesSinceQuery(t *testing.T) {
	mock := agent.NewMockClient().On("system.update_status", map[string]any{
		"unit":     "jabali-update-oneshot.service",
		"status":   "active",
		"log_tail": "→ install deps\n",
	})
	r := newAdminUpdatesRouter(mock, true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/updates/jabali/status?since=2026-04-25T10:00:00Z", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"active"`)
}

func TestAdminUpdates_Stop_CallsUnitStop(t *testing.T) {
	mock := agent.NewMockClient().On("system.unit_stop", map[string]any{"ok": true})
	r := newAdminUpdatesRouter(mock, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/updates/apt", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
}

// JAB-10: when the agent's system.apt_check reports a structured failure
// (apt lock etc.), the panel forwards it as a 200 body carrying the reason +
// hint (not a bare "agent_error"), so the admin card can render diagnostics.
func TestAdminUpdates_AptCheck_ForwardsStructuredError(t *testing.T) {
	mock := agent.NewMockClient().On("system.apt_check", map[string]any{
		"packages": []map[string]any{},
		"total":    0,
		"error": map[string]any{
			"reason":    "apt_locked",
			"command":   "apt-get update",
			"exit_code": 100,
			"stderr":    "E: Could not get lock /var/lib/dpkg/lock-frontend",
			"hint":      "A package operation is in progress. Wait, then retry.",
		},
	})
	r := newAdminUpdatesRouter(mock, true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/updates/apt/check", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"reason":"apt_locked"`)
	assert.Contains(t, rec.Body.String(), `"hint":`)
}
