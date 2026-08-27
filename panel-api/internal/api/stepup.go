package api

// JAB-380 — recent-auth (MFA step-up) for admin_root surfaces.
//
// The admin File Manager (whole-FS root scope) and the Root Terminal (root
// PTY) are the two most privileged panel surfaces. Beyond admin claims + their
// default-off opt-in settings, they require the caller's Kratos session to have
// authenticated *recently* (stepUpWindow). A stale session gets a 403 that the
// SPA turns into a Kratos `?refresh=true` re-login, after which it retries.
//
// This is deliberately not per-op server state: the window IS the grace window,
// so a burst of edits inside it doesn't re-prompt, and there is nothing to
// expire or leak.

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// stepUpWindow is how recently the Kratos session must have authenticated for a
// root-privileged action to proceed without a fresh login (JAB-380). It is the
// whole grace window (the check is stateless — see the file header), so this is
// also how long a burst of admin work can run before one re-auth. Raised from
// 10m to 1h (GH #1184): 10m re-prompted repeatedly during normal File Manager /
// Root Terminal use, while 1h still bounds a walked-up / hijacked idle session
// to an hour of root reach.
const stepUpWindow = 1 * time.Hour

// requireRecentAuth enforces JAB-380 recent-auth on a root-privileged request.
// It returns true if the request may proceed; otherwise it has already written
// the response and aborted, and the caller must return immediately.
//
// Rules (all fail closed):
//   - Only interactive Kratos cookie sessions can satisfy step-up. API-token
//     and automation-HMAC callers (Source != SourceKratos) have no interactive
//     session, so they are refused — a long-lived credential must never drive a
//     root shell or root-FS mutation.
//   - Impersonated (act-as, ADR-0128) requests are refused: the root surfaces
//     are unrelated to the impersonated tenant, and an admin must operate as
//     themselves here.
//   - The session's authenticated_at must be within stepUpWindow. The cached
//     claims value is checked first (fast path); on staleness one
//     cache-bypassing whoami is performed so a just-completed `?refresh=true`
//     login is recognised immediately instead of waiting out the ~10s whoami
//     cache TTL (which would otherwise loop the SPA back to refresh).
func requireRecentAuth(c *gin.Context, kc *kratosclient.Client, window time.Duration) bool {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return false
	}
	// Non-interactive credentials (API token / automation HMAC) and act-as
	// impersonation cannot step up. Distinct code so the SPA shows "use an
	// interactive session" rather than trying to launch a refresh flow.
	if claims.Source != auth.SourceKratos || claims.ImpersonatedBy != "" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":  "stepup_unavailable",
			"detail": "This action requires an interactive browser session with recent authentication.",
		})
		return false
	}

	now := time.Now()
	if recentEnough(claims.AuthenticatedAt, now, window) {
		return true
	}
	// Stale (or zero) cached value. Re-read fresh, bypassing the positive
	// whoami cache, so a session the user just refreshed via `?refresh=true`
	// is recognised without waiting out the cache TTL.
	if kc != nil {
		if cookie, err := c.Cookie("ory_kratos_session"); err == nil && cookie != "" {
			if ident, werr := kc.WhoamiUncached(c.Request.Context(), cookie); werr == nil &&
				recentEnough(ident.AuthenticatedAt, now, window) {
				return true
			}
		}
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error":       "stepup_required",
		"detail":      "Re-authenticate to perform this admin action.",
		"window_secs": int(window.Seconds()),
	})
	return false
}

// recentEnough reports whether authAt is non-zero and within window of now.
// A zero authAt (Kratos omitted it, or it failed to parse) fails closed.
func recentEnough(authAt, now time.Time, window time.Duration) bool {
	return !authAt.IsZero() && now.Sub(authAt) <= window
}
