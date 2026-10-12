package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

func TestHealthAgent_OK(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	m := agent.NewMockClient().On("agent.version", map[string]any{
		"version":        "0.1.0",
		"go_version":     "go1.25.1",
		"uptime_seconds": 3,
		"started_at":     "2026-04-16T00:00:00Z",
	})

	r := gin.New()
	api.RegisterAgentHealthRoute(r, m)

	req := httptest.NewRequest(http.MethodGet, "/health/agent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status string         `json:"status"`
		Agent  map[string]any `json:"agent"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body.Status)
	assert.Equal(t, "0.1.0", body.Agent["version"])
}

func TestHealthAgent_Unavailable503(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	m := agent.NewMockClient().OnError("agent.version", &agent.AgentError{
		Code:    agent.CodeUnavailable,
		Message: "dial failed",
	})

	r := gin.New()
	api.RegisterAgentHealthRoute(r, m)

	req := httptest.NewRequest(http.MethodGet, "/health/agent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"unavailable"`)
}

func TestHealthAgent_DeadlineExceeded504(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	m := agent.NewMockClient().OnError("agent.version", &agent.AgentError{
		Code:    agent.CodeDeadlineExceeded,
		Message: "timed out",
	})

	r := gin.New()
	api.RegisterAgentHealthRoute(r, m)

	req := httptest.NewRequest(http.MethodGet, "/health/agent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
}

func TestHealthAgent_UntypedError500(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	m := agent.NewMockClient().OnError("agent.version", errors.New("transport broke"))

	r := gin.New()
	api.RegisterAgentHealthRoute(r, m)

	req := httptest.NewRequest(http.MethodGet, "/health/agent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// An agent failure that isn't one of the mapped codes answers 500 with the
// agent's message, not 502: Cloudflare replaces an origin's 502 with its own
// error page, which hid the message on a panel behind it (GH #2077).
func TestHealthAgent_AgentFailure500KeepsTheMessage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	m := agent.NewMockClient().OnError("agent.version", &agent.AgentError{
		Code:    agent.CodeInternal,
		Message: "ufw: could not load the rules",
	})

	r := gin.New()
	api.RegisterAgentHealthRoute(r, m)

	req := httptest.NewRequest(http.MethodGet, "/health/agent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, string(agent.CodeInternal), body["error"])
	assert.Equal(t, "ufw: could not load the rules", body["detail"])
}
