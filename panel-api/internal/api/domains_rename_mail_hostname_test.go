package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390: a rename assigns a new domains.name, so it runs the same guard as
// create — the new name must not be, or be a parent zone of, the panel's
// custom mail hostname, whoever renames. The guard fires before the rename
// runs (no rename dependencies are wired, so reaching it would fail).

type renameTestDomains struct {
	repository.DomainRepository
	d *models.Domain
}

func (f renameTestDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return f.d, nil
}

func domainRenameRequest(t *testing.T, settings repository.ServerSettingsRepository, newName string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &domainHandler{cfg: DomainHandlerConfig{
		Domains:          renameTestDomains{d: &models.Domain{ID: "d1", Name: "old.example.org", UserID: "u1"}},
		WebDomainAliases: aliasTestAliases{},
		ServerSettings:   settings,
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
		c.Next()
	})
	r.POST("/domains/:id/rename", h.rename)
	req := httptest.NewRequest(http.MethodPost, "/domains/d1/rename", strings.NewReader(`{"name":"`+newName+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestDomainRename_MailHostnameCollision409(t *testing.T) {
	settings := aliasTestSettings{hostname: "panel.host.com", mailHostname: "mx.example.com"}
	for _, name := range []string{"mx.example.com", "example.com"} {
		rec := domainRenameRequest(t, settings, name)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "domain_conflicts_mail_hostname") {
			t.Fatalf("%s: want 409 domain_conflicts_mail_hostname, got %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestDomainRename_MailHostnameLookupFailsClosed(t *testing.T) {
	rec := domainRenameRequest(t, aliasTestSettings{err: errors.New("db down")}, "example.com")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "db_mail_hostname_lookup") {
		t.Fatalf("want 500 db_mail_hostname_lookup, got %d %s", rec.Code, rec.Body.String())
	}
}
