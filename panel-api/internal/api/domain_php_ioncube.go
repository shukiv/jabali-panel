package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainIonCubeHandlerConfig wires the per-domain ionCube enable route.
type DomainIonCubeHandlerConfig struct {
	Domains  repository.DomainRepository
	PHPPools repository.PHPPoolRepository
	Users    repository.UserRepository
	Agent    agent.AgentInterface
}

// RegisterDomainIonCubeRoutes adds the tenant-facing ionCube toggle:
//
//	PUT /domains/:id/php-ioncube   {"enabled": true|false}
//
// Ownership-scoped: a tenant may toggle ONLY their own domains (admin may
// toggle any). The loader-installed gate is enforced agent-side by
// php.ioncube.pool_set's precondition (translated to 409 here) so there is a
// single source of truth for "is the loader present for this version".
func RegisterDomainIonCubeRoutes(g *gin.RouterGroup, cfg DomainIonCubeHandlerConfig) {
	h := &domainIonCubeHandler{cfg: cfg}
	g.PUT("/domains/:id/php-ioncube", h.put)
}

type domainIonCubeHandler struct{ cfg DomainIonCubeHandlerConfig }

type domainIonCubeRequest struct {
	Enabled *bool `json:"enabled" binding:"required"`
}

func (h *domainIonCubeHandler) put(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req domainIonCubeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_argument", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "ioncube toggle: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Ownership: owner or admin only. Never let a tenant touch another's domain.
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Resolve the pool bound to this domain (or the owner's pool) → PHP version.
	// ionCube is per-(user × version); the domain's version determines which
	// pool's master the loader is toggled on.
	version := h.resolveVersion(ctx, dom)
	if version == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_php_version", "detail": "set a PHP version for this domain before enabling ionCube"})
		return
	}

	// Resolve the domain owner's unix username (the FPM master's %i). Admin
	// users have no username — but an admin-owned domain shouldn't reach here
	// with tenant PHP anyway.
	owner, err := h.cfg.Users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil || owner.Username == nil || *owner.Username == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_unix_account", "detail": "domain owner has no unix account for a PHP pool"})
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "php.ioncube.pool_set", map[string]any{
		"username": *owner.Username,
		"version":  version,
		"enabled":  *req.Enabled,
	})
	if err != nil {
		// The agent's precondition (loader not installed for this version)
		// surfaces as CodeFailedPrecondition → 409 with a clear message.
		status, body := translateAgentError(err)
		c.JSON(status, body)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// resolveVersion returns the PHP version driving this domain's pool: the bound
// pool's version if the domain is bound, else the owner's pool version
// (ADR-0023: one pool per user). Empty when neither exists.
func (h *domainIonCubeHandler) resolveVersion(ctx context.Context, dom *models.Domain) string {
	if dom.PHPPoolID != nil && *dom.PHPPoolID != "" {
		if pool, err := h.cfg.PHPPools.FindByID(ctx, *dom.PHPPoolID); err == nil && pool != nil {
			return pool.PHPVersion
		}
	}
	if pool, err := h.cfg.PHPPools.FindByUserID(ctx, dom.UserID); err == nil && pool != nil {
		return pool.PHPVersion
	}
	return ""
}
