package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type composerTestSettings struct {
	repository.ServerSettingsRepository
}

func (composerTestSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{}, nil
}

// GH #2276: the SPA shows a domain's Composer tab only when the user's
// package includes Composer runs; a user with no package gets none.
func TestServerCapabilities_ComposerFollowsThePackage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	name, pkgID := "alice", "pkg"
	for _, c := range []struct {
		label   string
		user    *models.User
		pkg     *models.HostingPackage
		enabled bool
	}{
		{"package with Composer", &models.User{ID: "u", Username: &name, PackageID: &pkgID}, &models.HostingPackage{ID: pkgID, ComposerEnabled: true}, true},
		{"package without Composer", &models.User{ID: "u", Username: &name, PackageID: &pkgID}, &models.HostingPackage{ID: pkgID}, false},
		{"no package", &models.User{ID: "u", Username: &name}, &models.HostingPackage{ID: pkgID, ComposerEnabled: true}, false},
	} {
		t.Run(c.label, func(t *testing.T) {
			r := gin.New()
			r.Use(func(ctx *gin.Context) { ginctx.SetClaims(ctx, &auth.AccessClaims{UserID: "u"}); ctx.Next() })
			RegisterMeRoutes(r.Group(""), MeHandlerConfig{
				Users:          &composerTestUsers{byID: map[string]*models.User{"u": c.user}},
				Packages:       &composerTestPackages{byID: map[string]*models.HostingPackage{pkgID: c.pkg}},
				ServerSettings: composerTestSettings{},
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/server-capabilities", nil))
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != http.StatusOK || body["composer_enabled"] != c.enabled {
				t.Fatalf("server-capabilities = %d composer_enabled=%v, want %v", w.Code, body["composer_enabled"], c.enabled)
			}
		})
	}
}

// GH #2277: the SPA shows a domain's Git tab only when the user's package
// includes Git deploy; a user with no package gets none.
func TestServerCapabilities_GitDeployFollowsThePackage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	name, pkgID := "alice", "pkg"
	for _, c := range []struct {
		label   string
		user    *models.User
		pkg     *models.HostingPackage
		enabled bool
	}{
		{"package with Git deploy", &models.User{ID: "u", Username: &name, PackageID: &pkgID}, &models.HostingPackage{ID: pkgID, GitDeployEnabled: true}, true},
		{"package without Git deploy", &models.User{ID: "u", Username: &name, PackageID: &pkgID}, &models.HostingPackage{ID: pkgID, ComposerEnabled: true}, false},
		{"no package", &models.User{ID: "u", Username: &name}, &models.HostingPackage{ID: pkgID, GitDeployEnabled: true}, false},
	} {
		t.Run(c.label, func(t *testing.T) {
			r := gin.New()
			r.Use(func(ctx *gin.Context) { ginctx.SetClaims(ctx, &auth.AccessClaims{UserID: "u"}); ctx.Next() })
			RegisterMeRoutes(r.Group(""), MeHandlerConfig{
				Users:          &composerTestUsers{byID: map[string]*models.User{"u": c.user}},
				Packages:       &composerTestPackages{byID: map[string]*models.HostingPackage{pkgID: c.pkg}},
				ServerSettings: composerTestSettings{},
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/server-capabilities", nil))
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != http.StatusOK || body["git_deploy_enabled"] != c.enabled {
				t.Fatalf("server-capabilities = %d git_deploy_enabled=%v, want %v", w.Code, body["git_deploy_enabled"], c.enabled)
			}
		})
	}
}
