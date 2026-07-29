package api

import (
	"bytes"
	"context"
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
)

// newAdminRouter wires a Gin router with admin claims injected and the
// extension routes registered against the provided mock agent.
func newAdminRouter(mock agent.AgentInterface) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})
	RegisterPHPExtensionAdminRoutes(v1, mock, extTestFetcher{})
	return r
}

// extTestFetcher is a no-op LoaderFetcher for the dpkg-extension tests (the
// ionCube apply paths have their own tests).
type extTestFetcher struct{}

func (extTestFetcher) FetchLoader(_ context.Context, _ string) ([]byte, error) {
	return []byte("fake-loader"), nil
}

func TestExtensions_List_HappyPath(t *testing.T) {
	t.Parallel()
	want := map[string]any{
		"version": "8.5",
		"extensions": []any{
			map[string]any{"name": "curl", "installed": true, "enabled": true, "built_in": false},
		},
	}
	mock := agent.NewMockClient().On("php.ext.list", want)
	r := newAdminRouter(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/versions/8.5/extensions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "8.5", body["version"])
}

func TestExtensions_List_BadVersionFormat_NoAgentCall(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient()
	r := newAdminRouter(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/versions/8/extensions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mock.Calls(), "agent must not be called on pre-validation failure")
}

func TestExtensions_List_AgentFailedPreconditionMapsTo409(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient().OnError("php.ext.list", &agent.AgentError{
		Code: agent.CodeFailedPrecondition, Message: "PHP 8.5 is not installed",
	})
	r := newAdminRouter(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/versions/8.5/extensions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, agent.CodeFailedPrecondition, body["error"])
}

func TestExtensions_Apply_HappyPath(t *testing.T) {
	t.Parallel()
	want := map[string]any{"version": "8.5", "ext": "curl", "installed": true, "enabled": true}
	mock := agent.NewMockClient().On("php.ext.apply", want)
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"install"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5/extensions/curl/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	calls := mock.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "php.ext.apply", calls[0].Command)
	// MockCall.Params is already a json.RawMessage — unmarshal directly.
	var params map[string]string
	require.NoError(t, json.Unmarshal(calls[0].Params, &params))
	assert.Equal(t, "8.5", params["version"])
	assert.Equal(t, "curl", params["ext"])
	assert.Equal(t, "install", params["action"])
}

func TestExtensions_Apply_BadAction_NoAgentCall(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient()
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"frobnicate"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5/extensions/curl/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mock.Calls())
}

func TestExtensions_Apply_UnknownExt_NoAgentCall(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient()
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"install"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5/extensions/nope/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, mock.Calls())
}

func TestExtensions_Apply_BadVersionFormat_NoAgentCall(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient()
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"install"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5.1/extensions/curl/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mock.Calls())
}

func TestExtensions_Apply_AgentInvalidArgumentMapsTo400(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient().OnError("php.ext.apply", &agent.AgentError{
		Code: agent.CodeInvalidArgument, Message: "extension posix is built in",
	})
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"install"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5/extensions/posix/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestExtensions_Apply_AgentFailedPreconditionMapsTo409(t *testing.T) {
	t.Parallel()
	mock := agent.NewMockClient().OnError("php.ext.apply", &agent.AgentError{
		Code: agent.CodeFailedPrecondition, Message: "cannot remove xml: still in use by dom",
	})
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"remove"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5/extensions/xml/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestExtensions_Apply_AgentInternalMapsTo502(t *testing.T) {
	t.Parallel()
	// translateAgentError defaults unknown *AgentError codes to 502 BadGateway.
	// CodeInternal is not in the explicit switch, so it falls through to 502.
	mock := agent.NewMockClient().OnError("php.ext.apply", &agent.AgentError{
		Code: agent.CodeInternal, Message: "dpkg-query exploded",
	})
	r := newAdminRouter(mock)

	body := bytes.NewBufferString(`{"action":"install"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/versions/8.5/extensions/curl/apply", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
}
