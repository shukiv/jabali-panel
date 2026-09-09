package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"github.com/gin-gonic/gin"
)

// GH #1627: 'custom' is the internal mail posture for a domain created from a
// custom DNS template. The live PATCH /domains/:id mail-provider switch must
// neither accept 'custom' as a target (it is only reachable by picking a
// template at create) nor let a template domain switch its posture away
// (that would strand the template's external apex MX/SPF beside a re-asserted
// Jabali apex → double SPF).
func mailProviderPatchRouter(dom *models.Domain) (*gin.Engine, *mockDomainRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: dom.UserID})
		c.Next()
	})
	repo := newMockDomainRepo()
	repo.domains[dom.ID] = dom
	settings := &mockServerSettingsRepo{getResult: &models.ServerSettings{}}
	RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: repo, ServerSettings: settings})
	return r, repo
}

func patchMailProvider(r *gin.Engine, id, provider string) *httptest.ResponseRecorder {
	body := bytes.NewBufferString(`{"mail_provider":"` + provider + `"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/"+id, body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestDomainPatch_MailProvider_CustomReserved(t *testing.T) {
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali}
	r, repo := mailProviderPatchRouter(dom)

	w := patchMailProvider(r, "d1", models.MailProviderCustom)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "mail_provider_custom_reserved") {
		t.Errorf("body = %q, want mail_provider_custom_reserved", w.Body.String())
	}
	if repo.domains["d1"].MailProvider != models.MailProviderJabali {
		t.Errorf("provider mutated to %q, must stay jabali", repo.domains["d1"].MailProvider)
	}
}

func TestDomainPatch_MailProvider_TemplatePostureLocked(t *testing.T) {
	for _, target := range []string{models.MailProviderJabali, models.MailProviderM365, models.MailProviderNone} {
		t.Run(target, func(t *testing.T) {
			dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderCustom}
			r, repo := mailProviderPatchRouter(dom)

			w := patchMailProvider(r, "d1", target)
			if w.Code != http.StatusConflict {
				t.Fatalf("code = %d, want 409: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "template_posture_locked") {
				t.Errorf("body = %q, want template_posture_locked", w.Body.String())
			}
			if repo.domains["d1"].MailProvider != models.MailProviderCustom {
				t.Errorf("provider switched to %q, must stay custom", repo.domains["d1"].MailProvider)
			}
		})
	}
}
