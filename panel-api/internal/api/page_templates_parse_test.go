package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakePageTemplateRepo records the last upserted content so tests can assert a
// rejected save never reached the store.
type fakePageTemplateRepo struct {
	lastContent  string
	upsertCalled bool
}

func (f *fakePageTemplateRepo) List(context.Context) ([]models.PageTemplate, error) {
	return nil, nil
}
func (f *fakePageTemplateRepo) Get(context.Context, string) (*models.PageTemplate, error) {
	return &models.PageTemplate{}, nil
}
func (f *fakePageTemplateRepo) Upsert(_ context.Context, _, content string) error {
	f.upsertCalled = true
	f.lastContent = content
	return nil
}
func (f *fakePageTemplateRepo) EnsureDefaults(context.Context) (int, error) { return 0, nil }

func newPageTemplatesRouter(repo *fakePageTemplateRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(true))
	api.RegisterPageTemplatesRoutes(v1, api.PageTemplatesHandlerConfig{Repo: repo})
	return r
}

// GH #860: a domain_default_index body that won't parse as a Go template must
// be rejected at save time — not silently accepted and then dropped by the
// agent (which falls back to its built-in default on every new domain).
func TestPageTemplates_RejectsUnparsableDefaultIndex(t *testing.T) {
	repo := &fakePageTemplateRepo{}
	r := newPageTemplatesRouter(repo)

	body := `{"content":"<h1>{{.Domain}}</h1><script>var x={{ oops }}</script>"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/admin/settings/page-templates/"+models.PageTemplateDomainDefaultIndex,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "template_parse_error")
	assert.False(t, repo.upsertCalled, "a broken template must not be persisted")
}

// A valid template with the supported placeholders saves normally.
func TestPageTemplates_AcceptsValidDefaultIndex(t *testing.T) {
	repo := &fakePageTemplateRepo{}
	r := newPageTemplatesRouter(repo)

	body := `{"content":"<h1>{{.Domain}}</h1><p>{{.DocRoot}} {{.Username}}</p>"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/admin/settings/page-templates/"+models.PageTemplateDomainDefaultIndex,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, repo.upsertCalled)
}

// Error pages are static HTML, not Go templates — a literal {{ }} in one is
// content, not a parse target, and must save without objection.
func TestPageTemplates_ErrorPageAllowsBraces(t *testing.T) {
	repo := &fakePageTemplateRepo{}
	r := newPageTemplatesRouter(repo)

	body := `{"content":"<html><body>404 {{ not a template }}</body></html>"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/admin/settings/page-templates/"+models.PageTemplateError404,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, repo.upsertCalled)
}
