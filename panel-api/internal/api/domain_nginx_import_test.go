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

func nginxImportRouter(userID string, isAdmin bool) (*gin.Engine, *mockDomainRepo) {
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
	RegisterDomainNginxImportRoutes(v1, DomainNginxImportHandlerConfig{Domains: dr})
	return r, dr
}

func postNginxImport(r *gin.Engine, id string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/"+id+"/nginx-import/preview", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestNginxImportPreview_OwnerConvertsSupportedShapes(t *testing.T) {
	r, dr := nginxImportRouter("owner", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postNginxImport(r, "dom1", map[string]any{
		"content": "rewrite ^/old$ /new permanent;\nlocation ~* \\.(env|sql)$ { deny all; }",
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp nginxImportPreviewResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Rules, 2)
	assert.Equal(t, "rewrite", resp.Rules[0].Type)
	assert.Equal(t, "deny_paths", resp.Rules[1].Type)
}

// The security-relevant test: a rule the converter produces but the TENANT
// validator would reject on save must be demoted to a warning here, never
// returned as an applicable rule. An absolute-URL rewrite (open-redirect shape)
// is the canonical case.
func TestNginxImportPreview_DemotesInvalidTenantRule(t *testing.T) {
	r, dr := nginxImportRouter("owner", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postNginxImport(r, "dom1", map[string]any{
		// rewrite to an absolute URL — validateTenantNginxRules refuses it
		// (must be a local path). It must NOT come back as an applicable rule.
		"content": "rewrite ^/go$ https://evil.example/x redirect;",
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp nginxImportPreviewResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp.Rules, "an absolute-URL rewrite must be demoted, not kept")
	require.NotEmpty(t, resp.Warnings)
	sawDemotion := false
	for _, wn := range resp.Warnings {
		if wn.Line == 0 { // demotion warnings carry Line 0
			sawDemotion = true
		}
	}
	assert.True(t, sawDemotion, "expected a tenant-validation demotion warning, got %+v", resp.Warnings)
}

func TestNginxImportPreview_ForbidsOtherTenant(t *testing.T) {
	r, dr := nginxImportRouter("intruder", false)
	dr.Create(context.Background(), &models.Domain{ID: "dom1", UserID: "owner", Name: "example.com"})

	w := postNginxImport(r, "dom1", map[string]any{"content": "rewrite ^/a$ /b;"})
	assert.Equal(t, http.StatusForbidden, w.Code)
}
