//go:build !demo

package app

import (
	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
)

// setupDemoAndInfo (production build) registers only the base /info route. The
// demo write-gate + credential-exposing /info do not exist in this binary — the
// demo variant lives in demo_on.go (//go:build demo). JAB-159 security boundary.
func setupDemoAndInfo(r *gin.Engine, _ *config.Config) {
	api.RegisterServiceInfoRoute(r)
}
