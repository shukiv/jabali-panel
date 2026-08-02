package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// adminRouter returns a Gin engine where the given group already carries
// admin claims, simulating what RequireAuth + RequireAdmin would do in
// production.
func adminRouter(cli agent.AgentInterface) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	// Inject admin claims so RequireAdmin passes.
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})
	api.RegisterSystemRoutes(v1, cli)
	return r
}

func userRouter(cli agent.AgentInterface) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-user", IsAdmin: false})
		c.Next()
	})
	api.RegisterSystemRoutes(v1, cli)
	return r
}

func TestSystemInfo_OK(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("system.info", map[string]any{
		"hostname":         "web01",
		"uptime_seconds":   86400,
		"load_avg":         [3]float64{0.5, 0.3, 0.2},
		"cpu_count":        4,
		"mem_total_kb":     16384000,
		"mem_available_kb": 8192000,
		"mem_used_kb":      8192000,
		"partitions":       []map[string]any{},
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "web01", body["hostname"])
	assert.Equal(t, float64(4), body["cpu_count"])
}

func TestSystemInfo_AgentUnavailable(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().OnError("system.info", &agent.AgentError{
		Code:    agent.CodeUnavailable,
		Message: "socket not found",
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestSystemInfo_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("system.info", map[string]any{})

	r := userRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSystemServices_OK(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("service.list", map[string]any{
		"services": []map[string]string{
			{"name": "nginx", "active": "active"},
			{"name": "mariadb", "active": "inactive"},
		},
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/services", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Services []struct {
			Name   string `json:"name"`
			Active string `json:"active"`
		} `json:"services"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Services, 2)
	assert.Equal(t, "nginx", body.Services[0].Name)
	assert.Equal(t, "active", body.Services[0].Active)
}

func TestSystemServices_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("service.list", map[string]any{})

	r := userRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/services", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSystemServicesRestart_OK(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("service.restart", map[string]any{
		"name":       "nginx",
		"active":     "active",
		"load_state": "loaded",
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/services/nginx/restart", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, m.Calls(), 1)
	assert.Equal(t, "service.restart", m.Calls()[0].Command)
}

// TestSystemServicesRestart_Masked — the agent returns
// CodeFailedPrecondition for masked units; the API should map that to
// 409 so the UI can render a "service is masked" message.
func TestSystemServicesRestart_Masked(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().OnError("service.restart", &agent.AgentError{
		Code:    agent.CodeFailedPrecondition,
		Message: "php8.5-fpm is masked; unmask via systemctl before restarting",
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/services/php8.5-fpm/restart", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestSystemServicesRestart_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient()
	r := userRouter(m)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/services/nginx/restart", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, m.Calls(), "non-admin must not reach agent")
}

func TestSystemResolversGet_OK(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("system.resolver.get", map[string]any{
		"active":        true,
		"resolvers":     []string{"1.1.1.1", "2606:4700:4700::1111"},
		"search_domain": "example.com",
		"source":        "drop-in",
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/resolvers", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "drop-in", body["source"])
	assert.Equal(t, true, body["active"])
}

func TestSystemResolversGet_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("system.resolver.get", map[string]any{})

	r := userRouter(m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/resolvers", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSystemResolversPut_OK(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().On("system.resolver.set", map[string]any{
		"active":        true,
		"resolvers":     []string{"1.1.1.1", "1.0.0.1"},
		"search_domain": "",
		"source":        "drop-in",
	})

	r := adminRouter(m)
	payload := `{"resolvers":["1.1.1.1","1.0.0.1"],"search_domain":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, m.Calls(), 1)
	assert.Equal(t, "system.resolver.set", m.Calls()[0].Command)

	// Confirm API forwarded the cleaned list.
	var forwardedParams struct {
		Resolvers    []string `json:"resolvers"`
		SearchDomain string   `json:"search_domain"`
	}
	require.NoError(t, json.Unmarshal(m.Calls()[0].Params, &forwardedParams))
	assert.Equal(t, []string{"1.1.1.1", "1.0.0.1"}, forwardedParams.Resolvers)
}

// TestSystemResolversPut_InvalidIP — malformed resolver never reaches the
// agent, the API returns 400 with a clear detail.
func TestSystemResolversPut_InvalidIP(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient()
	r := adminRouter(m)
	payload := `{"resolvers":["1.1.1.1","not-an-ip"]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, m.Calls(), "invalid input must not reach agent")
}

func TestSystemResolversPut_EmptyList(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient()
	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers", bytes.NewBufferString(`{"resolvers":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, m.Calls())
}

func TestSystemResolversPut_TooMany(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient()
	r := adminRouter(m)
	payload := `{"resolvers":["1.1.1.1","1.0.0.1","8.8.8.8","8.8.4.4","9.9.9.9","149.112.112.112","208.67.222.222","208.67.220.220","76.76.2.0"]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSystemResolversPut_Duplicate — panel rejects duplicates up front so
// the admin sees the error before the agent bounces.
func TestSystemResolversPut_Duplicate(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient()
	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers",
		bytes.NewBufferString(`{"resolvers":["1.1.1.1","1.1.1.1"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSystemResolversPut_AgentFailedPrecondition — when the agent reports
// the restart failed (and auto-rolled back), it returns CodeFailedPrecondition
// which translateAgentError maps to 409.
func TestSystemResolversPut_AgentFailedPrecondition(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient().OnError("system.resolver.set", &agent.AgentError{
		Code:    agent.CodeFailedPrecondition,
		Message: "systemd-resolved restart failed; rolled back drop-in",
	})

	r := adminRouter(m)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers",
		bytes.NewBufferString(`{"resolvers":["1.1.1.1"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestSystemResolversPut_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()

	m := agent.NewMockClient()
	r := userRouter(m)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/resolvers",
		bytes.NewBufferString(`{"resolvers":["1.1.1.1"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, m.Calls())
}
