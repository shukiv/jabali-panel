package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

func newSupportRouter(mock *agent.MockClient, isAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(isAdmin))
	api.RegisterAdminSupportRoutes(v1, api.AdminSupportHandlerConfig{Agent: mock})
	return r
}

func TestAdminSupport_RBAC(t *testing.T) {
	mock := agent.NewMockClient()
	r := newSupportRouter(mock, false)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/support/diagnostic", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAdminSupport_Diagnostic_HappyPath(t *testing.T) {
	mock := agent.NewMockClient().On("system.diagnostic_report", map[string]any{
		"url":             "https://enclosed.jabali-panel.com/01abc#pw:k",
		"password":        "supersecret",
		"note_id":         "01abc",
		"byte_count":      9999,
		"generated_at":    "2026-04-25T10:00:00Z",
		"redaction_count": 7,
		"file_count":      32,
	})
	r := newSupportRouter(mock, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/support/diagnostic", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"redaction_count":7`)
	assert.Contains(t, rec.Body.String(), `"password":"supersecret"`)
}

// GH #465 follow-up: the diagnostic endpoint is itself the diagnostic tool, so
// its failures must be stage-coded, not a generic "agent_error" — the UI turns
// these codes into actionable text (egress problem vs dead agent vs timeout).
func TestAdminSupport_Diagnostic_ErrorCodes(t *testing.T) {
	cases := []struct {
		name     string
		agentErr error
		wantCode string
	}{
		{"enclosed unreachable", &agent.AgentError{Code: agentwire.CodeUpstreamUnavailable, Message: "enclosed upload: dial tcp: timeout"}, "upload_unreachable"},
		{"agent deadline", &agent.AgentError{Code: agentwire.CodeDeadlineExceeded, Message: "deadline"}, "agent_timeout"},
		{"agent internal", &agent.AgentError{Code: agentwire.CodeInternal, Message: "collect: boom"}, "agent_error"},
		{"agent socket down", &agent.AgentError{Code: agentwire.CodeUnavailable, Message: "dial agent: no such file"}, "agent_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := agent.NewMockClient().OnError("system.diagnostic_report", tc.agentErr)
			r := newSupportRouter(mock, true)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/support/diagnostic", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Contains(t, rec.Body.String(), `"error":"`+tc.wantCode+`"`)
			// Leak-suppression: the agent's message must not reach the client.
			assert.NotContains(t, rec.Body.String(), "dial")
			assert.NotContains(t, rec.Body.String(), "boom")
		})
	}
}
