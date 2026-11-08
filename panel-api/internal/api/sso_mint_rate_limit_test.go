package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// Every route that mints a one-time sign-in link sits behind SSORateLimit;
// the routes next to them that mint nothing do not.
func TestSSOMintRoutes_RateLimited(t *testing.T) {
	limited := func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) }
	r := gin.New()
	r.Use(gin.Recovery(), func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterSSOPhpMyAdminRoutes(v1, SSOPhpMyAdminHandlerConfig{SSORateLimit: limited})
	RegisterSSOAdminerRoutes(v1, SSOAdminerHandlerConfig{SSORateLimit: limited})
	RegisterDatabaseAdminOpsRoutes(v1, DatabaseAdminOpsHandlerConfig{
		DBAdmin:      &fakeDBAdmin{},
		SSO:          &fakeAdminTokenMinter{},
		AdminerSSO:   &fakeAdminerTokenMinter{},
		SSORateLimit: limited,
	})
	RegisterMailboxRoutes(v1, MailboxHandlerConfig{SSORateLimit: limited})
	RegisterMagicLinkRoutes(v1, MagicLinkHandlerConfig{SSORateLimit: limited})

	hit := func(method, path string) int {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}
	for _, p := range []string{
		"/api/v1/sso/phpmyadmin",
		"/api/v1/sso/adminer",
		"/api/v1/admin/databases/sso/phpmyadmin",
		"/api/v1/admin/databases/sso/adminer",
		"/api/v1/mailboxes/mb1/sso",
		"/api/v1/applications/app1/magic-link",
	} {
		if code := hit(http.MethodPost, p); code != http.StatusTooManyRequests {
			t.Errorf("POST %s: want 429 from the SSO limit, got %d", p, code)
		}
	}
	for _, p := range []string{
		"/api/v1/mailboxes/mb1/rotate-password",
		"/api/v1/admin/databases/maintenance",
	} {
		if code := hit(http.MethodPost, p); code == http.StatusTooManyRequests {
			t.Errorf("POST %s mints no sign-in link and must not be behind the SSO limit", p)
		}
	}
}
