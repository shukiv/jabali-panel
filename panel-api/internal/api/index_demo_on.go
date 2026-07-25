//go:build demo

package api

import "github.com/gin-gonic/gin"

// DemoInfo is the public projection of config.DemoConfig surfaced via /info so
// the SPA can render the DEMO banner + "Enter as admin/user" buttons. Passwords
// are deliberately included — a demo instance is a public service and visitors
// need the seeded credentials. This type exists ONLY in a `-tags demo` build.
type DemoInfo struct {
	Enabled       bool   `json:"enabled"`
	AdminEmail    string `json:"admin_email,omitempty"`
	AdminPassword string `json:"admin_password,omitempty"`
	UserEmail     string `json:"user_email,omitempty"`
	UserPassword  string `json:"user_password,omitempty"`
	Banner        string `json:"banner,omitempty"`
}

// demoInfo is set by RegisterServiceInfoRouteWithDemo when demo mode is enabled.
var demoInfo DemoInfo

// RegisterServiceInfoRouteWithDemo is the demo-mode variant of the /info route:
// the payload additionally carries the DemoInfo block. Exists only in a demo build.
func RegisterServiceInfoRouteWithDemo(r *gin.Engine, di DemoInfo) {
	demoInfo = di
	r.GET("/info", infoHandler)
}

func injectDemo(resp gin.H) {
	if demoInfo.Enabled {
		resp["demo"] = demoInfo
	}
}
