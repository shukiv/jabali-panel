package app

import (
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
)

// Every route that mints a one-time sign-in link is wired with the SSO limit
// (rl.SSOPerActor): it carries one handler more than a route beside it in the
// same group that has no per-route middleware.
func TestSSOMintRoutesCarryTheSSOLimit(t *testing.T) {
	handlers := map[string]int{}
	prev := gin.DebugPrintRouteFunc
	gin.DebugPrintRouteFunc = func(method, path, _ string, n int) {
		handlers[method+" "+path] = n
	}
	t.Cleanup(func() { gin.DebugPrintRouteFunc = prev })

	cfg := config.Defaults()
	cfg.Server.Env = config.EnvDevelopment // debug mode: gin reports each route as it is registered
	cfg.Auth.Kratos.PublicURL = "http://127.0.0.1:4433"
	cfg.Auth.Kratos.AdminURL = "http://127.0.0.1:4434"
	NewWithDeps(cfg, fullDeps())

	for _, p := range []struct{ mint, sibling string }{
		{"POST /api/v1/sso/phpmyadmin", "GET /api/v1/me"},
		{"POST /api/v1/sso/adminer", "GET /api/v1/me"},
		{"POST /api/v1/applications/:id/magic-link", "GET /api/v1/me"},
		{"POST /api/v1/mailboxes/:mbid/sso", "POST /api/v1/mailboxes/:mbid/rotate-password"},
		{"POST /api/v1/admin/databases/sso/phpmyadmin", "POST /api/v1/admin/databases/maintenance"},
		{"POST /api/v1/admin/databases/sso/adminer", "POST /api/v1/admin/databases/maintenance"},
	} {
		mint, ok := handlers[p.mint]
		if !ok {
			t.Errorf("%s not registered", p.mint)
			continue
		}
		sibling, ok := handlers[p.sibling]
		if !ok {
			t.Errorf("%s not registered", p.sibling)
			continue
		}
		if mint != sibling+1 {
			t.Errorf("%s has %d handlers, want %d (%s's %d plus the SSO limit)", p.mint, mint, sibling+1, p.sibling, sibling)
		}
	}
}
