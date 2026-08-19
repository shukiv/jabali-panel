package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
)

// SSOPhpMyAdminHandlerConfig plugs the phpMyAdmin SSO handler into the router.
type SSOPhpMyAdminHandlerConfig struct {
	Databases repository.DatabaseRepository
	SSO       *sso.Service
	Log       *slog.Logger
	SSOConfig config.SSOConfig
}

// RegisterSSOPhpMyAdminRoutes mounts the POST /api/v1/sso/phpmyadmin endpoint.
func RegisterSSOPhpMyAdminRoutes(g *gin.RouterGroup, cfg SSOPhpMyAdminHandlerConfig) {
	h := &ssoPhpMyAdminHandler{cfg: cfg}
	g.POST("/sso/phpmyadmin", h.issueSSOToken)
}

type ssoPhpMyAdminHandler struct{ cfg SSOPhpMyAdminHandlerConfig }

type ssoPhpMyAdminRequest struct {
	DatabaseID string `json:"database_id" binding:"required"`
}

type ssoPhpMyAdminResponse struct {
	RedirectURL string `json:"redirect_url"`
}

// issueSSOToken handles POST /api/v1/sso/phpmyadmin.
// Auth: JWT. CSRF: same-origin check. Body: {"database_id":"<ulid>"}.
// Returns: {"redirect_url":"<absolute-url>/phpmyadmin/sso.php?token=<base64url>&db=<name>"}.
func (h *ssoPhpMyAdminHandler) issueSSOToken(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		h.auditLog(ctx, "", "", "", "unauthorized:no_session")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// CSRF: same-origin check via Origin/Referer headers
	if !h.validateSameOrigin(c) {
		h.cfg.Log.InfoContext(ctx, "sso_phpmyadmin same-origin check failed",
			"user_id", claims.UserID,
			"origin", c.GetHeader("Origin"),
			"referer", c.GetHeader("Referer"),
			"x_forwarded_proto", c.GetHeader("X-Forwarded-Proto"),
			"host", c.Request.Host,
			"computed", h.getOrigin(c),
		)
		h.auditLog(ctx, claims.UserID, "", "", "unauthorized:same_origin")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req ssoPhpMyAdminRequest
	if err := c.BindJSON(&req); err != nil {
		h.auditLog(ctx, claims.UserID, "", "", "unauthorized:bad_json")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}

	// Verify ownership: database.user_id == JWT sub
	db, err := h.cfg.Databases.FindByID(ctx, req.DatabaseID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.auditLog(ctx, claims.UserID, req.DatabaseID, "", "unauthorized:db_not_found")
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		h.cfg.Log.ErrorContext(ctx, "database lookup failed", "err", err)
		h.auditLog(ctx, claims.UserID, req.DatabaseID, "", "unauthorized:db_lookup_error")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if db.UserID != claims.UserID {
		h.auditLog(ctx, claims.UserID, req.DatabaseID, "", "unauthorized:owner_mismatch")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Ensure shadow account and get credentials
	if err := h.cfg.SSO.EnsureShadow(ctx, claims.UserID); err != nil {
		h.cfg.Log.ErrorContext(ctx, "ensure shadow account failed", "err", err)
		h.auditLog(ctx, claims.UserID, req.DatabaseID, "", "unauthorized")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Mint SSO token
	token, err := h.cfg.SSO.MintToken(ctx, claims.UserID, req.DatabaseID, db.Name)
	if err != nil {
		h.cfg.Log.ErrorContext(ctx, "mint token failed", "err", err)
		h.auditLog(ctx, claims.UserID, req.DatabaseID, "", "unauthorized")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Hash the DECODED token bytes with the same prefix length the validate
	// handler uses, so "issued" and "validated"/"unauthorized" lines for one
	// token share a prefix and can actually be correlated.
	//
	// This previously hashed the base64url STRING and logged 8 bytes, while
	// validate hashes the raw bytes and logs 4 — a different digest at a
	// different length, so the two halves of the SSO audit chain could never
	// be joined. Not exploitable, but it broke the trail exactly when it
	// matters: during an incident.
	hashPrefix := ssoTokenHashPrefix(token)
	h.auditLog(ctx, claims.UserID, req.DatabaseID, hashPrefix, "issued")

	// Build redirect URL with absolute base
	baseURL := h.getPhpMyAdminBaseURL(c)
	query := url.Values{}
	query.Set("token", token)
	query.Set("db", db.Name)
	redirectURL := baseURL + "/phpmyadmin/sso.php?" + query.Encode()

	c.JSON(http.StatusOK, ssoPhpMyAdminResponse{RedirectURL: redirectURL})
}

// getPhpMyAdminBaseURL derives the base URL for phpMyAdmin redirects.
// Priority:
//  1. Explicit sso.phpmyadmin_base_url config (use as-is)
//  2. Browser-supplied Origin header (preserves the visible port —
//     nginx fronts phpMyAdmin on the same vhost as the panel, so the
//     panel's :8443 is also where /phpmyadmin lives)
//  3. Referer header (same parse path)
//  4. Last resort: scheme + Request.Host (port stripped by nginx,
//     redirects to :443 — only useful when phpMyAdmin actually lives
//     on the default vhost)
//
// Earlier builds always took path 4 and tripped over the M6.4 panel-
// hostname mail topology, where the :443 default vhost is the
// webmail — phpMyAdmin SSO redirected operators to Roundcube.
func (h *ssoPhpMyAdminHandler) getPhpMyAdminBaseURL(c *gin.Context) string {
	if h.cfg.SSOConfig.PhpMyAdminBaseURL != "" {
		return strings.TrimSuffix(h.cfg.SSOConfig.PhpMyAdminBaseURL, "/")
	}

	// Origin / Referer headers carry the URL the browser is on,
	// including port — exactly what we want for the redirect target.
	for _, raw := range []string{c.GetHeader("Origin"), c.GetHeader("Referer")} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}

	// Fallback: derive from forwarded scheme + Host. nginx strips port
	// from $host so this URL won't preserve :8443 — kept only for
	// non-browser callers (curl tests, agent loopbacks). Panel is
	// https-only, so default to https unless X-Forwarded-Proto says
	// otherwise.
	scheme := "https"
	if fp := c.GetHeader("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	}
	return scheme + "://" + hostnameOf(c.Request.Host)
}

// auditLog emits a structured slog line for SSO operations. Logged at
// info-level — the production log already keeps token-hash prefixes
// only (never the token itself), and a 'forbidden' outcome is the
// kind of signal an operator wants to see without flipping the
// global level to debug.
func (h *ssoPhpMyAdminHandler) auditLog(ctx context.Context, userID, databaseID, tokenHashPrefix, outcome string) {
	h.cfg.Log.InfoContext(ctx, "sso_phpmyadmin",
		"user_id", userID,
		"database_id", databaseID,
		"token_hash_prefix", tokenHashPrefix,
		"outcome", outcome,
	)
}

// validateSameOrigin checks that Origin or Referer header matches the request host.
// Rejects cross-origin state-changing requests.
//
// Compares hostname-only (not full Origin string) because nginx's
// `proxy_set_header Host $host` strips the port from Request.Host —
// browsers send Origin with the visible port (`:8443`), so a strict
// string comparison always fails on the panel's nginx-fronted topology.
// Hostname + scheme is the actual same-origin invariant we need.
func (h *ssoPhpMyAdminHandler) validateSameOrigin(c *gin.Context) bool {
	// One shared exact same-origin policy across every DB-console SSO mint
	// (JAB-304). Behaviour is unchanged for this already-strict path.
	return sameOriginStrict(c)
}

// urlMatchesHost parses raw and compares its hostname (port stripped)
// to c.Request.Host's hostname (also port-stripped). Used for both
// Origin and Referer matching.
func (h *ssoPhpMyAdminHandler) urlMatchesHost(c *gin.Context, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return hostnameOf(u.Host) == hostnameOf(c.Request.Host)
}

// hostnameOf strips any trailing :port from a Host string. Returns the
// hostname half. Bracket-wrapped IPv6 literals are preserved
// (`[::1]:8080` → `[::1]`).
func hostnameOf(host string) string {
	if host == "" {
		return ""
	}
	// IPv6 literal in brackets: keep brackets, strip the port after them.
	if host[0] == '[' {
		if end := strings.LastIndex(host, "]"); end != -1 {
			return host[:end+1]
		}
		return host
	}
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		return host[:idx]
	}
	return host
}

func (h *ssoPhpMyAdminHandler) getOrigin(c *gin.Context) string {
	// panel-api listens on a unix socket behind nginx (M25); TLS is
	// terminated upstream so c.Request.TLS is always nil. The vhost
	// template sets `X-Forwarded-Proto: https`, so honour that header
	// before falling back to TLS-on-socket detection.
	scheme := "http"
	if fp := c.GetHeader("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// refererMatchesHost is retained for backwards compatibility with any
// downstream tests; new code should call urlMatchesHost.
func (h *ssoPhpMyAdminHandler) refererMatchesHost(c *gin.Context, referer string) bool {
	return h.urlMatchesHost(c, referer)
}
