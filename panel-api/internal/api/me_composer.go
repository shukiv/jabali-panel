// me_composer.go — per-user Composer version channel (GH #1332 item 13).
//
// The host `composer` is a dispatcher that picks a phar from the tenant's
// chosen channel. These self-service routes let a user select it:
//
//	GET /api/v1/me/composer-channel          { channel }   ("latest" default)
//	PUT /api/v1/me/composer-channel {channel}              ("" | "latest" | "lts")
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MeComposerConfig carries the deps the composer-channel routes need.
type MeComposerConfig struct {
	Users repository.UserRepository
	Agent agent.AgentInterface
}

func validComposerChannel(ch string) bool {
	switch ch {
	case "", "latest", "lts":
		return true
	}
	return false
}

// RegisterMeComposerRoutes mounts the self-service Composer channel routes on a
// group that already carries auth.
func RegisterMeComposerRoutes(g *gin.RouterGroup, cfg MeComposerConfig) {
	if cfg.Users == nil {
		return
	}
	h := &meComposerHandler{cfg: cfg}
	g.GET("/me/composer-channel", h.get)
	g.PUT("/me/composer-channel", h.put)
}

type meComposerHandler struct{ cfg MeComposerConfig }

func (h *meComposerHandler) get(c *gin.Context) {
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
	channel := "latest"
	if user.ComposerChannel != nil && *user.ComposerChannel != "" {
		channel = *user.ComposerChannel
	}
	c.JSON(http.StatusOK, gin.H{"channel": channel})
}

type putComposerReq struct {
	Channel string `json:"channel"` // "" | "latest" | "lts"
}

func (h *meComposerHandler) put(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req putComposerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if !validComposerChannel(req.Channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_channel", "detail": "channel must be latest or lts"})
		return
	}
	user, err := h.cfg.Users.FindByID(ctx, claims.UserID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	username := ""
	if user.Username != nil {
		username = *user.Username
	}
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_shell_user"})
		return
	}
	// Normalise "" -> "latest" for storage as NULL (the default).
	var stored *string
	if req.Channel == "lts" {
		lts := "lts"
		stored = &lts
	}
	if err := h.cfg.Users.UpdateComposerChannel(ctx, claims.UserID, stored); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Set("audit_target", "composer_channel:"+username)
	if _, err := h.cfg.Agent.Call(ctx, "php.composer_default_set", map[string]string{
		"username": username,
		"channel":  req.Channel,
	}); err != nil {
		respondAgentErr(c, "composer_channel_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": req.Channel})
}
