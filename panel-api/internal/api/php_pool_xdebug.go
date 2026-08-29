package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// php_pool_xdebug.go — GH #1332 item 9. Per-(user, PHP version) Xdebug toggle
// (safe modes only: develop/coverage/profile — no remote step-debug). Xdebug is
// a Zend extension loaded once per FPM master, so this is pool-scoped.

// PHPXdebugHandlerConfig wires the per-pool Xdebug routes.
type PHPXdebugHandlerConfig struct {
	Agent               agent.AgentInterface
	Users               repository.UserRepository
	Packages            repository.PackageRepository
	PHPPools            repository.PHPPoolRepository
	PHPPoolIniOverrides repository.PHPPoolIniOverrideRepository
}

func RegisterPHPXdebugRoutes(g *gin.RouterGroup, cfg PHPXdebugHandlerConfig) {
	h := &phpXdebugHandler{cfg: cfg}
	g.GET("/me/php-xdebug", h.get)
	g.PUT("/me/php-xdebug", h.set)
}

type phpXdebugHandler struct{ cfg PHPXdebugHandlerConfig }

func (h *phpXdebugHandler) owner(c *gin.Context) (*models.User, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		return nil, false
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || user.PackageID == nil {
		return user, false
	}
	pkg, err := h.cfg.Packages.FindByID(c.Request.Context(), *user.PackageID)
	if err != nil || pkg == nil {
		return user, false
	}
	return user, pkg.FpmUserCanEdit
}

func (h *phpXdebugHandler) pool(c *gin.Context, userID, version string) (*models.PHPPool, bool) {
	ctx := c.Request.Context()
	if version != "" {
		if p, err := h.cfg.PHPPools.FindByUserAndVersion(ctx, userID, version); err == nil && p != nil {
			return p, true
		}
		return nil, false
	}
	if p, err := h.cfg.PHPPools.FindByUserID(ctx, userID); err == nil && p != nil {
		return p, true
	}
	return nil, false
}

func (h *phpXdebugHandler) get(c *gin.Context) {
	user, canEdit := h.owner(c)
	if user == nil || !canEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "fpm_editing_not_allowed"})
		return
	}
	pool, ok := h.pool(c, user.ID, c.Query("php_version"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"php_version": pool.PHPVersion, "enabled": pool.XdebugEnabled})
}

type setXdebugRequest struct {
	PHPVersion string `json:"php_version"`
	Enabled    bool   `json:"enabled"`
}

func (h *phpXdebugHandler) set(c *gin.Context) {
	user, canEdit := h.owner(c)
	if user == nil || !canEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "fpm_editing_not_allowed"})
		return
	}
	var req setXdebugRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	pool, ok := h.pool(c, user.ID, req.PHPVersion)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
		return
	}
	pool.XdebugEnabled = req.Enabled
	pool.Status = "pending"
	if err := h.cfg.PHPPools.Update(c.Request.Context(), pool); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Set("audit_target", "php_xdebug:"+pool.PHPVersion)
	go reconcilePHPPoolViaAgent(h.cfg.Agent, h.cfg.Users, h.cfg.PHPPoolIniOverrides, h.cfg.PHPPools, pool)
	c.JSON(http.StatusOK, gin.H{"enabled": pool.XdebugEnabled})
}
