//go:build demo

package app

import (
	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// setupDemoAndInfo (demo build, `-tags demo`). When cfg.Demo.Enabled, mount the
// write-block middleware and register the credential-carrying /info so the SPA
// can render demo affordances. Still runtime-gated: a demo binary with
// Demo.Enabled=false behaves like production.
func setupDemoAndInfo(r *gin.Engine, cfg *config.Config) {
	if cfg.Demo.Enabled {
		r.Use(middleware.DemoMode(nil, cfg.Demo.Banner))
		api.RegisterServiceInfoRouteWithDemo(r, api.DemoInfo{
			Enabled:       true,
			AdminEmail:    cfg.Demo.AdminEmail,
			AdminPassword: cfg.Demo.AdminPassword,
			UserEmail:     cfg.Demo.UserEmail,
			UserPassword:  cfg.Demo.UserPassword,
			Banner:        cfg.Demo.Banner,
		})
		return
	}
	api.RegisterServiceInfoRoute(r)
}
