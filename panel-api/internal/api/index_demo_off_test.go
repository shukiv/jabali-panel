//go:build !demo

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// In a production build (no `demo` tag) the /info endpoint must NEVER carry a
// "demo" block — the credential-exposing route + injectDemo variant are compiled
// out. This is the JAB-159 security boundary, asserted at the HTTP layer.
func TestInfo_ProdBuild_NoDemoBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterServiceInfoRoute(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/info", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasDemo := body["demo"]
	require.False(t, hasDemo, "prod /info must not expose a demo block")
	require.NotContains(t, rec.Body.String(), "admin_password")
}
