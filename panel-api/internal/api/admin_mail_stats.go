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
	"sort"
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

// DomainTrafficRow is one domain's traffic totals over the selected range
// (GH #873 round 3). Powers the per-domain traffic drilldown — "which domains
// generate the mail volume".
type DomainTrafficRow struct {
	Domain    string `json:"domain"`
	Sent      int64  `json:"sent"`
	Received  int64  `json:"received"`
	Delivered int64  `json:"delivered"`
	Failed    int64  `json:"failed"`
}

type mailStatsResponse struct {
	// Points and Rates are keyed by metric name.
	Points  map[string][]MailStatPoint     `json:"points"`
	Rates   map[string][]MailStatPoint     `json:"rates"`
	Current map[string]float64             `json:"current"`
	Storage []repository.MailboxStorageRow `json:"storage"`
	// Traffic is the per-domain send/receive breakdown for the range, busiest
	// first. Empty until the per-domain sampler has collected a window.
	Traffic []DomainTrafficRow `json:"traffic"`
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
	domainRows, err := h.cfg.Stats.DomainSeries(c.Request.Context(), since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "traffic failed"})
		return
	}

	resp := mailStatsResponse{
		Points:  map[string][]MailStatPoint{},
		Rates:   map[string][]MailStatPoint{},
		Current: map[string]float64{},
		Storage: storage,
		Traffic: aggregateDomainTraffic(domainRows),
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

// aggregateDomainTraffic sums the per-domain delta samples into one row per
// domain over the whole range, busiest first (by sent+received), ties broken
// by domain name for a stable listing.
func aggregateDomainTraffic(rows []repository.DomainStatSample) []DomainTrafficRow {
	byDomain := map[string]*DomainTrafficRow{}
	for _, r := range rows {
		d := byDomain[r.Domain]
		if d == nil {
			d = &DomainTrafficRow{Domain: r.Domain}
			byDomain[r.Domain] = d
		}
		switch r.Metric {
		case "sent":
			d.Sent += r.Value
		case "received":
			d.Received += r.Value
		case "delivered":
			d.Delivered += r.Value
		case "failed":
			d.Failed += r.Value
		}
	}
	out := make([]DomainTrafficRow, 0, len(byDomain))
	for _, d := range byDomain {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].Sent+out[i].Received, out[j].Sent+out[j].Received
		if li != lj {
			return li > lj
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}
