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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
)

type SSOAdminerHandlerConfig struct {
	Databases repository.DatabaseRepository
	SSO       *sso.Service
	Adminer   *sso.AdminerService
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

	engine := strings.TrimSpace(db.Engine)
	if engine == "" {
		engine = "mariadb"
	}
	switch engine {
	case "mariadb":
		if err := h.cfg.SSO.EnsureShadow(ctx, claims.UserID); err != nil {
			h.cfg.Log.ErrorContext(ctx, "ensure mariadb shadow failed", "err", err)
			h.audit(ctx, claims.UserID, req.DatabaseID, "", engine, "ensure_shadow_fail")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	case "postgres":
		if err := h.cfg.Adminer.EnsurePgShadow(ctx, claims.UserID); err != nil {
			h.cfg.Log.ErrorContext(ctx, "ensure pg shadow failed", "err", err)
			h.audit(ctx, claims.UserID, req.DatabaseID, "", engine, "ensure_shadow_fail")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	default:
		h.audit(ctx, claims.UserID, req.DatabaseID, "", engine, "unauthorized:unknown_engine")
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_engine"})
		return
	}

	token, err := h.cfg.Adminer.MintAdminerToken(ctx, claims.UserID, req.DatabaseID, engine)
	if err != nil {
		h.cfg.Log.ErrorContext(ctx, "mint adminer token failed", "err", err)
		h.audit(ctx, claims.UserID, req.DatabaseID, "", engine, "mint_fail")
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
	h.audit(ctx, claims.UserID, req.DatabaseID, hashPrefix, engine, "issued")

	baseURL := h.getAdminerBaseURL(c)
	q := url.Values{}
	q.Set("token", token)
	q.Set("db", db.Name)
	q.Set("engine", engine)
	c.JSON(http.StatusOK, ssoAdminerResponse{
		RedirectURL: baseURL + "/jabali-adminer/?" + q.Encode(),
	})
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
	origin := c.GetHeader("Origin")
	referer := c.GetHeader("Referer")
	if origin != "" {
		return h.urlMatchesHost(c, origin)
	}
	if referer != "" {
		return h.urlMatchesHost(c, referer)
	}
	return false
}

func (h *ssoAdminerHandler) urlMatchesHost(c *gin.Context, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return hostnameOf(u.Host) == hostnameOf(c.Request.Host)
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
