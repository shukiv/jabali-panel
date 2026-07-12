// me_php_cli.go — per-user CLI default PHP version (GH #256).
//
// PHP version is per-domain, but a shell user has a single `php` on PATH. These
// self-service routes let a user choose which version a bare `php` / composer /
// wp-cli resolves to, independent of any domain. The choice is persisted on the
// user row and pushed to the host via the agent's php.cli_default_set verb.
//
//	GET /api/v1/me/php-cli-version          { version }   ("" = auto)
//	PUT /api/v1/me/php-cli-version  {version}             ("" clears → auto)
package api

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MePhpCliConfig carries the deps the CLI-version routes need.
type MePhpCliConfig struct {
	Users repository.UserRepository
	Agent agent.AgentInterface
}

var cliPhpVersionRe = regexp.MustCompile(`^\d+\.\d+$`)

// RegisterMePhpCliRoutes mounts the self-service CLI PHP version routes on a
// group that already carries auth.
func RegisterMePhpCliRoutes(g *gin.RouterGroup, cfg MePhpCliConfig) {
	if cfg.Users == nil {
		return
	}
	h := &mePhpCliHandler{cfg: cfg}
	g.GET("/me/php-cli-version", h.get)
	g.PUT("/me/php-cli-version", h.put)
}

type mePhpCliHandler struct{ cfg MePhpCliConfig }

func (h *mePhpCliHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	version := ""
	if user.CLIPHPVersion != nil {
		version = *user.CLIPHPVersion
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

type putCliVersionReq struct {
	Version string `json:"version"` // "" = auto (clear)
}

func (h *mePhpCliHandler) put(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req putCliVersionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if req.Version != "" && !cliPhpVersionRe.MatchString(req.Version) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_version", "detail": "must be like 8.3"})
		return
	}
	user, err := h.cfg.Users.FindByID(ctx, claims.UserID)
	if err != nil || user == nil || user.Username == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "no_shell_user", "detail": "account has no Linux user"})
		return
	}

	// Push to the host FIRST — the agent rejects an uninstalled version, so a
	// bad choice never reaches the DB. version "" clears the choice → auto.
	if h.cfg.Agent != nil {
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, aerr := h.cfg.Agent.Call(agentCtx, "php.cli_default_set", map[string]any{
			"username": *user.Username,
			"version":  req.Version,
		}); aerr != nil {
			respondAgentErr(c, "agent_error", aerr)
			return
		}
	}

	var stored *string
	if req.Version != "" {
		v := req.Version
		stored = &v
	}
	if err := h.cfg.Users.UpdateCLIPHPVersion(ctx, claims.UserID, stored); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": req.Version})
}
