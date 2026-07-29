package api

import (
	"context"
	"encoding/json"
	"errors"
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

type fakeFetcher struct {
	data []byte
	err  error
}

func (f fakeFetcher) FetchLoader(_ context.Context, _ string) ([]byte, error) {
	return f.data, f.err
}

func icAdminRouter(mock agent.AgentInterface, fetcher LoaderFetcher) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
		c.Next()
	})
	RegisterIonCubeAdminRoutes(v1, mock, fetcher)
	return r
}

func TestIonCubeAdmin_Status_MergesCatalogue(t *testing.T) {
	mock := agent.NewMockClient().On("php.ioncube.status", map[string]any{
		"installed_versions": []any{"8.4"},
		"enabled":            map[string]any{"arizote": []any{"8.4"}},
	})
	r := icAdminRouter(mock, fakeFetcher{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/php/ioncube", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body, "supported_versions")
	assert.Equal(t, "15.5.0", body["loader_version"])
	assert.Equal(t, []any{"8.4"}, body["installed_versions"])
}

func TestIonCubeAdmin_Install_FetchesAndCallsAgent(t *testing.T) {
	mock := agent.NewMockClient().On("php.ioncube.install", map[string]any{"version": "8.4", "installed": true, "changed": true})
	r := icAdminRouter(mock, fakeFetcher{data: []byte("\x7fELF loader")})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/ioncube/versions/8.4/install", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestIonCubeAdmin_Install_BadVersion_NoFetch(t *testing.T) {
	// version 8.0 is not in the catalogue → 400 before any fetch/agent call.
	r := icAdminRouter(agent.NewMockClient(), fakeFetcher{err: errors.New("should not be called")})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/ioncube/versions/8.0/install", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestIonCubeAdmin_Install_FetchFailure_502(t *testing.T) {
	r := icAdminRouter(agent.NewMockClient(), fakeFetcher{err: errors.New("network down")})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/php/ioncube/versions/8.4/install", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestIonCubeAdmin_Uninstall_BlockedTranslates409(t *testing.T) {
	mock := agent.NewMockClient().OnError("php.ioncube.uninstall", &agent.AgentError{
		Code:    agent.CodeFailedPrecondition,
		Message: "still enabled on 1 pool(s)",
	})
	r := icAdminRouter(mock, fakeFetcher{})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/php/ioncube/versions/8.4", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code)
}
