package api

// GH #873 round 4 — tenant-scoped mail traffic. A tenant sees the per-domain
// send/receive breakdown for ONLY their own domains. The scope is enforced at
// the SQL layer (DomainSeriesForUser filters on domains.user_id = caller), and
// the caller id is always taken from the authenticated claims, never the
// request — a tenant can never widen it to another tenant's domains.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type TenantMailStatsHandlerConfig struct {
	Stats repository.MailStatsRepository
}

// RegisterTenantMailStatsRoutes mounts GET /mail/stats for authenticated
// tenants. `g` is expected to already carry tenant auth + the mail-module gate.
func RegisterTenantMailStatsRoutes(g *gin.RouterGroup, cfg TenantMailStatsHandlerConfig) {
	if cfg.Stats == nil {
		return
	}
	h := &tenantMailStatsHandler{cfg: cfg}
	g.GET("/mail/stats", h.stats)
}

type tenantMailStatsHandler struct{ cfg TenantMailStatsHandlerConfig }

type tenantMailStatsResponse struct {
	// Traffic is the caller's per-domain totals over the range, busiest first.
	Traffic []DomainTrafficRow `json:"traffic"`
}

func (h *tenantMailStatsHandler) stats(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	hours := 24
	if raw := c.Query("hours"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 2160 {
			hours = v
		}
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	rows, err := h.cfg.Stats.DomainSeriesForUser(c.Request.Context(), since, claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "traffic failed"})
		return
	}
	c.JSON(http.StatusOK, tenantMailStatsResponse{Traffic: aggregateDomainTraffic(rows)})
}
