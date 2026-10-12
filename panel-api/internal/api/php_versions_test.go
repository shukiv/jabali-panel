package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

func TestPHPVersions_HappyPath(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject authenticated claims (non-admin user)
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-user", IsAdmin: false})
		c.Next()
	})

	mockAgent := agent.NewMockClient().On("php.version.list", map[string]interface{}{
		"versions": []string{"8.1", "8.3"},
	})

	RegisterPHPVersionRoutes(v1, mockAgent)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/versions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotNil(t, body["versions"])
	versions := body["versions"].([]interface{})
	assert.Len(t, versions, 2)
}

func TestPHPVersions_AgentError(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject authenticated claims (admin user)
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})

	mockAgent := agent.NewMockClient().OnError("php.version.list", &agent.AgentError{
		Code:    "command_failed",
		Message: "PHP agent unreachable",
	})

	RegisterPHPVersionRoutes(v1, mockAgent)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/versions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotNil(t, body["error"])
}

func TestPHPVersionAdmin_Status_HappyPath(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject admin user
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})

	admin := v1.Group("/admin", middleware.RequireAdmin())

	mockAgent := agent.NewMockClient().On("php.version.status", map[string]interface{}{
		"default_version": "8.5",
		"versions": []map[string]interface{}{
			{"version": "8.5", "installed": true, "fpm_running": true},
			{"version": "8.4", "installed": false, "fpm_running": false},
		},
	})

	RegisterPHPVersionAdminRoutes(admin, mockAgent, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/php/versions/status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "8.5", body["default_version"])
}

func TestPHPVersionAdmin_Install_HappyPath(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})

	admin := v1.Group("/admin", middleware.RequireAdmin())

	mockAgent := agent.NewMockClient().On("php.version.install", map[string]interface{}{
		"version":     "8.4",
		"installed":   true,
		"fpm_running": true,
	})

	RegisterPHPVersionAdminRoutes(admin, mockAgent, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/php/versions/8.4/install", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// Install is now asynchronous (GH #293): the handler returns 202 + a
	// status immediately and runs the agent install in the background; the SPA
	// polls /admin/php/versions/status until the version reports installed.
	require.Equal(t, http.StatusAccepted, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "8.4", body["version"])
	assert.Equal(t, "installing", body["status"])
}

func TestPHPVersionAdmin_Install_InvalidVersion(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})

	admin := v1.Group("/admin", middleware.RequireAdmin())
	mockAgent := agent.NewMockClient()

	RegisterPHPVersionAdminRoutes(admin, mockAgent, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/php/versions/5.6/install", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "unsupported version")
}

func TestPHPVersionAdmin_Reload_HappyPath(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})

	admin := v1.Group("/admin", middleware.RequireAdmin())

	mockAgent := agent.NewMockClient().On("php.version.reload", map[string]interface{}{
		"version": "8.5",
		"message": "reload successful",
	})

	RegisterPHPVersionAdminRoutes(admin, mockAgent, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/php/versions/8.5/reload", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "8.5", body["version"])
}
