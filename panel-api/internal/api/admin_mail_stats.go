package api

// GH #873: admin mail statistics — history series from the mail stats
// ticker's samples plus the mailbox-storage drilldown.
//
// The samples are stored name-agnostically (whatever Stalwart's exporter
// emits), so the endpoint returns, per metric:
//   - points: the raw sampled values (right for gauges: queue_size,
//     *_active_connections, server_memory, ...)
//   - rates:  positive deltas between consecutive samples (right for
//     monotonic counters: message_ingest, auth_success, ...; clamped at 0
//     so a Stalwart restart's counter reset shows as a flat spot, not a
//     negative spike)
// The SPA knows which representation each chart wants.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type AdminMailStatsHandlerConfig struct {
	Stats repository.MailStatsRepository
}

func RegisterAdminMailStatsRoutes(g *gin.RouterGroup, cfg AdminMailStatsHandlerConfig) {
	h := &adminMailStatsHandler{cfg: cfg}
	grp := g.Group("/admin/mail")
	grp.Use(middleware.RequireAdmin())
	grp.GET("/stats", h.stats)
}

type adminMailStatsHandler struct{ cfg AdminMailStatsHandlerConfig }

// MailStatPoint is one charted point.
type MailStatPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

type mailStatsResponse struct {
	// Points and Rates are keyed by metric name.
	Points  map[string][]MailStatPoint     `json:"points"`
	Rates   map[string][]MailStatPoint     `json:"rates"`
	Current map[string]float64             `json:"current"`
	Storage []repository.MailboxStorageRow `json:"storage"`
}

func (h *adminMailStatsHandler) stats(c *gin.Context) {
	hours := 24
	if raw := c.Query("hours"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 2160 {
			hours = v
		}
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	rows, err := h.cfg.Stats.Series(c.Request.Context(), since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "series failed"})
		return
	}
	storage, err := h.cfg.Stats.StorageDrilldown(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage failed"})
		return
	}

	resp := mailStatsResponse{
		Points:  map[string][]MailStatPoint{},
		Rates:   map[string][]MailStatPoint{},
		Current: map[string]float64{},
		Storage: storage,
	}
	// rows arrive ordered (metric, time) — walk once.
	for i := 0; i < len(rows); i++ {
		r := rows[i]
		resp.Points[r.Metric] = append(resp.Points[r.Metric], MailStatPoint{T: r.SampledAt, V: r.Value})
		resp.Current[r.Metric] = r.Value
		if i > 0 && rows[i-1].Metric == r.Metric {
			delta := r.Value - rows[i-1].Value
			if delta < 0 {
				delta = 0 // counter reset (Stalwart restart)
			}
			resp.Rates[r.Metric] = append(resp.Rates[r.Metric], MailStatPoint{T: r.SampledAt, V: delta})
		}
	}
	c.JSON(http.StatusOK, resp)
}
