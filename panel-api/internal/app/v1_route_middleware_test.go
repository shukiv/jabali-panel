package app

import (
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
)

// GH #2159: gin gives a route the group's middleware as it is when the route
// is registered. /me/api-tokens was registered on v1 before
// ResolveImpersonation, the audit and the standby guard were added, so it ran
// without them. Every /me route sits on v1, so none may carry fewer handlers
// than GET /me, which is registered after the last v1.Use.
func TestV1MeRoutesCarryTheFullMiddlewareChain(t *testing.T) {
	type route struct {
		method, path string
		handlers     int
	}
	var routes []route
	prev := gin.DebugPrintRouteFunc
	gin.DebugPrintRouteFunc = func(method, path, _ string, n int) {
		routes = append(routes, route{method, path, n})
	}
	t.Cleanup(func() { gin.DebugPrintRouteFunc = prev })

	cfg := config.Defaults()
	cfg.Server.Env = config.EnvDevelopment // debug mode: gin reports each route as it is registered
	cfg.Auth.Kratos.PublicURL = "http://127.0.0.1:4433"
	cfg.Auth.Kratos.AdminURL = "http://127.0.0.1:4434"
	NewWithDeps(cfg, fullDeps())

	base := -1
	for _, r := range routes {
		if r.method == "GET" && r.path == "/api/v1/me" {
			base = r.handlers
		}
	}
	if base < 0 {
		t.Fatalf("GET /api/v1/me not registered (%d routes seen)", len(routes))
	}
	var short []string
	for _, r := range routes {
		if (r.path == "/api/v1/me" || strings.HasPrefix(r.path, "/api/v1/me/")) && r.handlers < base {
			short = append(short, r.method+" "+r.path)
		}
	}
	sort.Strings(short)
	if len(short) > 0 {
		t.Fatalf("routes registered before the last v1.Use (fewer than %d handlers):\n%s", base, strings.Join(short, "\n"))
	}
}
