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

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func htaccessRouter(userID string, isAdmin bool) (*gin.Engine, *mockDomainRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
			c.Next()
		})
	}
	dr := newMockDomainRepo()
	RegisterDomainHtaccessRoutes(v1, DomainHtaccessHandlerConfig{Domains: dr})
	return r, dr
}

func postPreview(r *gin.Engine, id string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/"+id+"/htaccess/preview", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHtaccessPreview_OwnerConverts(t *testing.T) {
	r, dr := htaccessRouter("owner", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postPreview(r, "dom1", map[string]any{
		"content": "Redirect 301 /old https://example.com/new\nRequire valid-user",
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp htaccessPreviewResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	// the Redirect line goes to the domain's Redirects list, not the Rule
	// Builder; the auth line is a warning.
	assert.Empty(t, resp.Rules)
	require.Len(t, resp.Redirects, 1)
	assert.Equal(t, models.PageRedirect{Source: "/old", Destination: "https://example.com/new", Type: "301", KeepQuery: true, TrailingSlash: true}, resp.Redirects[0])
	assert.NotEmpty(t, resp.Warnings)
	assert.Empty(t, resp.Invalid, "converter must emit only valid rules")
}

func TestHtaccessPreview_DenyAllIsValid(t *testing.T) {
	r, dr := htaccessRouter("admin", true)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postPreview(r, "dom1", map[string]any{
		"content":   "Order Deny,Allow\nDeny from all",
		"base_path": "/private/",
	})
	require.Equal(t, http.StatusOK, w.Code)
	var resp htaccessPreviewResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Rules, 1)
	assert.Equal(t, "ip_access", resp.Rules[0].Type)
	assert.Equal(t, "allow_list", resp.Rules[0].Mode)
	assert.Empty(t, resp.Rules[0].IPs) // deny-all
	assert.Empty(t, resp.Invalid)      // and it passes the apply-path validator
}

func TestHtaccessPreview_NonOwnerForbidden(t *testing.T) {
	r, dr := htaccessRouter("intruder", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postPreview(r, "dom1", map[string]any{"content": "Redirect 301 /a /b"})
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHtaccessPreview_MissingContent(t *testing.T) {
	r, dr := htaccessRouter("owner", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postPreview(r, "dom1", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHtaccessPreview_NotFound(t *testing.T) {
	r, _ := htaccessRouter("owner", false)
	w := postPreview(r, "nope", map[string]any{"content": "Redirect 301 /a /b"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHtaccessPreview_NoRulesEmitsEmptyArrays(t *testing.T) {
	r, dr := htaccessRouter("owner", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	// Front-controller-only input -> 0 rules. The JSON must carry [] not null
	// so the SPA can read .length/.filter without crashing.
	w := postPreview(r, "dom1", map[string]any{
		"content": "RewriteCond %{REQUEST_FILENAME} !-f\nRewriteCond %{REQUEST_FILENAME} !-d\nRewriteRule . /index.php [L]",
	})
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"rules":[]`)
	assert.NotContains(t, body, `"rules":null`)
	assert.NotContains(t, body, `"warnings":null`)
	assert.NotContains(t, body, `"notes":null`)
}
