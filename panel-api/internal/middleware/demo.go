//go:build demo

// Package middleware — DemoMode.
//
// DemoMode short-circuits every non-idempotent HTTP method
// (POST/PUT/PATCH/DELETE) under /api/v1/* with HTTP 403 +
// {"error":"demo_mode","detail":"<banner copy>"} so a public demo
// instance lets visitors browse every read endpoint without ever
// reaching the agent or the database write path.
//
// The middleware is intentionally narrow:
//
//   - GET / HEAD / OPTIONS always pass through (reads + CORS preflight)
//   - Any path NOT under /api/v1/ passes through (Kratos browser-flow
//     login lives under /.ory/*, /sso/* is the SSO bridge, /info /
//     /health are read-only metadata)
//   - Caller-supplied allowedPrefixes always pass through (defensive
//     hook for future panel-side login endpoints, currently unused —
//     the Kratos browser flow lives outside /api/v1/)
//
// Mounted only when cfg.Demo.Enabled = true. Off by default; production
// installs leave it off and panel-api behaves exactly as before.
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// DemoMode returns the gin middleware. allowedPrefixes is the list of
// path prefixes that bypass the write-block (always empty in current
// deployments; reserved for future panel-side auth endpoints). banner
// is the operator-visible explanation surfaced in the JSON error
// payload — empty falls back to a sane default so the SPA can render a
// useful toast even if config.toml omits the field.
func DemoMode(allowedPrefixes []string, banner string) gin.HandlerFunc {
	if banner == "" {
		banner = "Demo mode — changes are disabled"
	}
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api/v1/") {
			c.Next()
			return
		}
		for _, prefix := range allowedPrefixes {
			if prefix != "" && strings.HasPrefix(path, prefix) {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":  "demo_mode",
			"detail": banner,
		})
	}
}