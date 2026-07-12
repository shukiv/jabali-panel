package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// EnforceSameOriginForCookieMutations is a global CSRF guard for the
// cookie-authenticated API surface (GH #460).
//
// The panel authenticates browsers with a Kratos session cookie (SameSite=Lax).
// Lax blocks the cookie on most cross-site subrequests, but leaves gaps
// (top-level POST within the browser's Lax-allow window, method confusion), so
// this adds an explicit same-origin check as defense-in-depth before any
// state-changing handler.
//
// Scope:
//   - Safe methods (GET/HEAD/OPTIONS/TRACE) are never blocked.
//   - Requests carrying an `Authorization: Bearer …` token are exempt: a
//     cross-origin page cannot set a custom Authorization header without a CORS
//     preflight it can't satisfy, so token/automation callers are not
//     browser-CSRF-able. Only cookie auth is, and only that path is checked.
//   - For a cookie-authenticated mutating request, the Origin header (or
//     Referer when Origin is absent) must be the same origin as the request —
//     scheme + host + port all match (RFC 6454). A cross-site attacker cannot
//     forge a matching Origin from the victim's browser, so a mismatch — or a
//     request with neither header — is rejected.
//
// Mounted on the /api/v1 group, so it covers every authenticated route
// including the admin subgroups; the separately-mounted HMAC automation group
// is unaffected (signed requests are not CSRF-able).
func EnforceSameOriginForCookieMutations() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			c.Next()
			return
		}
		if ah := c.GetHeader("Authorization"); strings.HasPrefix(ah, "Bearer ") || strings.HasPrefix(ah, "bearer ") {
			c.Next()
			return
		}
		if !requestIsSameOrigin(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf_origin_mismatch"})
			return
		}
		c.Next()
	}
}

// requestIsSameOrigin reports whether the request's Origin (or, when absent,
// Referer) is the same origin as the request itself per the RFC 6454 tuple:
// scheme + host + port must all match (JAB-115). The authority comparison
// (host[:port], exact) mirrors middleware.isSameOrigin — the CORS gate — and
// the scheme is pinned on top so a same-hostname-different-port or
// http-vs-https origin is rejected, not accepted. Both headers absent → false
// (a genuine browser always sends at least one on a cross-/same-origin
// mutation).
func requestIsSameOrigin(c *gin.Context) bool {
	reqScheme := requestScheme(c)
	reqHost := c.Request.Host
	for _, raw := range []string{c.GetHeader("Origin"), c.GetHeader("Referer")} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Scheme == "" {
			return false
		}
		// u.Host is host[:port]; compare the full authority exactly and pin
		// the scheme. Browsers omit the default port in Origin, so a request
		// served on 443 (Host "panel") matches Origin "https://panel".
		return strings.EqualFold(u.Scheme, reqScheme) && u.Host == reqHost
	}
	return false
}

// requestScheme resolves the scheme the client used to reach the panel. Behind
// the panel's own nginx, X-Forwarded-Proto is authoritative (nginx overwrites
// any client-supplied value with $scheme, so a CSRF browser can't forge it);
// fall back to the TLS state for direct connections.
func requestScheme(c *gin.Context) string {
	if xfp := c.GetHeader("X-Forwarded-Proto"); xfp != "" {
		if i := strings.IndexByte(xfp, ','); i >= 0 {
			xfp = xfp[:i] // first hop wins on a proxy chain
		}
		return strings.ToLower(strings.TrimSpace(xfp))
	}
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}
