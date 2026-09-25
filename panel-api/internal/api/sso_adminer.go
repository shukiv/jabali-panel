// Adminer SSO mint handler — POST /api/v1/sso/adminer.
//
// Engine-agnostic mirror of sso_phpmyadmin.go. Derives the engine from
// the database row (mariadb | postgres), provisions the matching
// shadow account on first use, and mints a single-use Adminer token.
// Returns a redirect URL pointing at the jabali-adminer vhost — the
// Adminer jabali-sso plugin reads the token, posts to the validate
// UDS endpoint, and gets the engine-specific credentials back.
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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type SSOAdminerHandlerConfig struct {
	Databases repository.DatabaseRepository
	// SSO/Adminer are typed as the dbconsoleops shadow+mint interfaces (not the
	// concrete *sso.Service/*sso.AdminerService) so the door exposes a mint seam
	// for the AC4 contract matrix. SSO provides the mariadb shadow; Adminer
	// provides the postgres shadow + the Adminer token mint — the Shadow/PgShadow
	// split dbconsoleops.Issue selects between. The concrete services satisfy both
	// and are what the router wires in (JAB-348).
	SSO       dbconsoleops.ShadowService
	Adminer   dbconsoleops.AdminerConsole
	Log       *slog.Logger
	SSOConfig config.SSOConfig
}

func RegisterSSOAdminerRoutes(g *gin.RouterGroup, cfg SSOAdminerHandlerConfig) {
	h := &ssoAdminerHandler{cfg: cfg}
	g.POST("/sso/adminer", h.issueSSOToken)
}

type ssoAdminerHandler struct{ cfg SSOAdminerHandlerConfig }

type ssoAdminerRequest struct {
	DatabaseID string `json:"database_id" binding:"required"`
}

type ssoAdminerResponse struct {
	RedirectURL string `json:"redirect_url"`
}

func (h *ssoAdminerHandler) issueSSOToken(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		h.audit(ctx, "", "", "", "", "unauthorized:no_session")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if !h.validateSameOrigin(c) {
		h.audit(ctx, claims.UserID, "", "", "", "unauthorized:same_origin")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req ssoAdminerRequest
	if err := c.BindJSON(&req); err != nil {
		h.audit(ctx, claims.UserID, "", "", "", "unauthorized:bad_json")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}

	db, err := h.cfg.Databases.FindByID(ctx, req.DatabaseID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.audit(ctx, claims.UserID, req.DatabaseID, "", "", "unauthorized:db_not_found")
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		h.cfg.Log.ErrorContext(ctx, "database lookup failed", "err", err)
		h.audit(ctx, claims.UserID, req.DatabaseID, "", "", "unauthorized:db_lookup_error")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if db.UserID != claims.UserID {
		h.audit(ctx, claims.UserID, req.DatabaseID, "", db.Engine, "unauthorized:owner_mismatch")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// The DB Console SSO module owns the rest (JAB-348): engine normalization,
	// the shadow account for the engine (mariadb → SSO, postgres → Adminer), the
	// mint, the redirect and the audit hash-prefix. This door owns the transport:
	// status codes and its audit labels (ssoIssueFailure).
	res, err := dbconsoleops.Issue(ctx, dbconsoleops.IssueDeps{
		Shadow: h.cfg.SSO, PgShadow: h.cfg.Adminer, Adminer: h.cfg.Adminer,
	}, dbconsoleops.IssueRequest{
		Scope:      dbconsoleops.ScopeDatabase,
		UserID:     claims.UserID,
		DatabaseID: req.DatabaseID,
		DBName:     db.Name,
		Engine:     db.Engine,
		Console:    dbconsoleops.ConsoleAdminer,
		BaseURL:    h.getAdminerBaseURL(c),
	})
	if err != nil {
		status, code, outcome := ssoIssueFailure(err)
		if status == http.StatusInternalServerError {
			h.cfg.Log.ErrorContext(ctx, "adminer sso issuance failed", "outcome", outcome, "err", err)
		}
		h.audit(ctx, claims.UserID, req.DatabaseID, "", res.Engine, outcome)
		c.JSON(status, gin.H{"error": code})
		return
	}
	h.audit(ctx, claims.UserID, req.DatabaseID, res.HashPrefix, res.Engine, dbconsoleops.OutcomeIssued)

	c.JSON(http.StatusOK, ssoAdminerResponse{RedirectURL: res.LoginURL})
}

func (h *ssoAdminerHandler) getAdminerBaseURL(c *gin.Context) string {
	if h.cfg.SSOConfig.AdminerBaseURL != "" {
		return strings.TrimSuffix(h.cfg.SSOConfig.AdminerBaseURL, "/")
	}
	for _, raw := range []string{c.GetHeader("Origin"), c.GetHeader("Referer")} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	scheme := "https"
	if fp := c.GetHeader("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	}
	return scheme + "://" + hostnameOf(c.Request.Host)
}

func (h *ssoAdminerHandler) validateSameOrigin(c *gin.Context) bool {
	// One shared exact same-origin policy across every DB-console SSO mint
	// (JAB-304). Behaviour is unchanged for this already-strict path.
	return sameOriginStrict(c)
}

func (h *ssoAdminerHandler) audit(ctx context.Context, userID, databaseID, hashPrefix, engine, outcome string) {
	h.cfg.Log.InfoContext(ctx, "sso_adminer",
		"user_id", userID,
		"database_id", databaseID,
		"engine", engine,
		"token_hash_prefix", hashPrefix,
		"outcome", outcome,
	)
}
