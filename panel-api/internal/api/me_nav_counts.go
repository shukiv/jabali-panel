package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// me_nav_counts.go — GH #1478 side-nav badge counts.
//
// One lightweight aggregate endpoint per side so the nav fetches all its
// badge numbers in a single call rather than firing a list query per item:
//   GET /me/nav-counts     — the caller's own resource counts (tenant nav)
//   GET /admin/nav-counts  — fleet-wide counts (admin nav; RequireAdmin)
//
// Web/Mail/DNS follow the GH #1449 service flags (web-enabled / mail-enabled /
// dns-hosted domains). The counts are cheap aggregates; the UI caches the
// result and refreshes on a short interval, so this is not a hot path.

// NavCountsConfig wires the badge-count endpoints.
type NavCountsConfig struct {
	Counts repository.NavCountsRepository
}

type navCountsHandler struct{ cfg NavCountsConfig }

// RegisterNavCountsRoutes mounts the tenant + admin badge-count endpoints on an
// authenticated group. The admin route additionally requires admin.
func RegisterNavCountsRoutes(g *gin.RouterGroup, cfg NavCountsConfig) {
	h := &navCountsHandler{cfg: cfg}
	g.GET("/me/nav-counts", h.me)
	g.GET("/admin/nav-counts", middleware.RequireAdmin(), h.admin)
}

func (h *navCountsHandler) me(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	counts, err := h.cfg.Counts.ForUser(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, counts)
}

func (h *navCountsHandler) admin(c *gin.Context) {
	counts, err := h.cfg.Counts.Global(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, counts)
}
