package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterServiceInfoRoute wires GET /info — a lightweight JSON endpoint
// that returns version / service metadata. `/` is owned by the SPA (Phase
// 8+), so operators who want a machine-readable status hit /info or
// /health instead.
func RegisterServiceInfoRoute(r *gin.Engine) {
	r.GET("/info", infoHandler)
}

func infoHandler(c *gin.Context) {
	resp := gin.H{
		"service": "jabali-panel",
		"version": Version,
		"status":  "ok",
		"endpoints": gin.H{
			"health": "/health",
			"api":    "/api/v1/",
			"ui":     "/ (SPA)",
		},
		"docs": "https://github.com/shukiv/jabali-panel",
	}
	// injectDemo is a no-op in production builds and adds the seeded-credential
	// "demo" block only in a `-tags demo` build (JAB-159). See index_demo_*.go.
	injectDemo(resp)
	c.JSON(http.StatusOK, resp)
}

// RegisterMethodNotAllowedHandler installs a JSON response for NoMethod.
// NoRoute is owned by the SPA static-file handler (internal/webui) which
// serves index.html as a fallback for client-side routes and JSON 404s for
// API-ish paths.
func RegisterMethodNotAllowedHandler(r *gin.Engine) {
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{
			"error":  "method_not_allowed",
			"method": c.Request.Method,
			"path":   c.Request.URL.Path,
		})
	})
}
