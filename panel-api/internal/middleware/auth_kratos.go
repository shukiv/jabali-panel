package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// RequireKratosSession validates a Kratos browser session cookie and populates
// the request-scoped AccessClaims with the authenticated PANEL user's fields.
//
// The middleware runs two lookups on every cache-miss:
//
//  1. GET {kratos}/sessions/whoami — validates the cookie and returns the
//     Kratos identity (UUID + traits).
//  2. users WHERE kratos_identity_id = <UUID> — maps that Kratos identity
//     back to the panel user row created by the M20 step 4 migration tool
//     (or the POST /api/v1/users hook for new users).
//
// The panel's existing authz uses users.id (ULID) as the owner key on every
// resource (domains, databases, applications, …). If we left claims.UserID as
// the Kratos UUID, every ownership check would return 403 post-cutover. So
// the claims.UserID we stash is the PANEL id, with Kratos's traits confirmed
// against the panel row (is_admin in particular must match the DB, not the
// trait cached server-side in Kratos).
//
// On auth failure we return 401 with a specific reason ("missing_session" |
// "invalid_session" | "identity_not_linked"). On Kratos infrastructure
// failures (network / 5xx / timeout) we return 503 so a transient upstream
// blip doesn't force every user to re-login. Authorization headers are
// ignored in Kratos mode — the cookie is the only credential source,
// closing adversarial-review finding #1 from plans/m20-kratos-identity.md.
func RequireKratosSession(kratosClient *kratosclient.Client, users repository.UserRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie("ory_kratos_session")
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":   "missing_session",
				"message": "Kratos session cookie not found",
			})
			c.Abort()
			return
		}

		identity, err := kratosClient.Whoami(c.Request.Context(), cookie)
		if err != nil {
			if errors.Is(err, kratosclient.ErrUnauthenticated) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"error":   "invalid_session",
					"message": "Kratos session validation failed",
				})
			} else {
				// Infrastructure failure (network, 5xx, timeout). Returning 401 here
				// would force a user-visible re-login on every Kratos blip; instead
				// report 503 so the SPA can show a transient-error toast and retry.
				// Error details are logged server-side but never leaked to clients.
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"error":   "identity_service_unavailable",
					"message": "identity service temporarily unavailable",
				})
			}
			c.Abort()
			return
		}

		// Resolve Kratos identity → panel user row. An identity the migration
		// tool hasn't processed yet (no panel row with kratos_identity_id =
		// identity.ID) is treated as unauthenticated: the session is real but
		// there's no panel account for it to map to, so every resource check
		// would fail anyway. 401 tells the SPA to re-login — which, for the
		// common case of a mid-migration race, completes fine on retry.
		panelUser, err := users.FindByKratosIdentityID(c.Request.Context(), identity.ID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"error":   "identity_not_linked",
					"message": "Kratos identity has no corresponding panel user — contact an administrator",
				})
			} else {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"error":   "identity_lookup_failed",
					"message": "could not resolve identity to panel user",
				})
			}
			c.Abort()
			return
		}

		// A suspended account must not authenticate (Gitea #522). Kratos
		// inactive state usually blocks the session, but enforce it from our
		// own DB so a suspend takes effect regardless of Kratos propagation.
		if panelUser.Suspended {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account_suspended", "message": "this account is suspended"})
			c.Abort()
			return
		}
		// Prefer the panel row's fields as the source of truth: is_admin must
		// match what our own DB says, not the trait Kratos happened to have
		// cached, so an admin demotion takes effect on the next request
		// regardless of Kratos-side propagation.
		claims := &auth.AccessClaims{
			UserID:  panelUser.ID,
			Email:   panelUser.Email,
			IsAdmin: panelUser.IsAdmin,
			// JAB-380: carry the session's last-authenticated time + assurance
			// level so recent-auth gates (root File Manager / Root Terminal)
			// can require a fresh login. Source stays SourceKratos (zero value).
			AuthenticatedAt: identity.AuthenticatedAt,
			AAL:             identity.AAL,
		}

		ginctx.SetClaims(c, claims)
		c.Next()
	}
}
