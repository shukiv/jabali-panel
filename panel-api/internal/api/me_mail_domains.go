package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// me_mail_domains.go — GH #1387 foundation slice: a tenant-facing per-domain
// mail summary, the drill-down entry point (Mail → Mail Domains → accounts).
//
// Read-only, owner-scoped, pure DB (no agent call): domains via ListByUserID,
// mailbox count/storage via ListByOwnerWithDomain (both owner-scoped), 30-day
// sent/received via MailStatsRepository.DomainSeriesForUser (owner-scoped). The
// current mail queue (a per-domain Stalwart query) and the account drill-down /
// tab migration are the operator's mail restructure and are deliberately out of
// this slice.

type MeMailDomainsConfig struct {
	Domains   repository.DomainRepository
	Mailboxes repository.MailboxRepository
	MailStats repository.MailStatsRepository
}

// RegisterMeMailDomainsRoutes mounts GET /me/mail-domains on an auth-only group.
func RegisterMeMailDomainsRoutes(g *gin.RouterGroup, cfg MeMailDomainsConfig) {
	h := &meMailDomainsHandler{cfg: cfg}
	g.GET("/me/mail-domains", h.list)
}

type meMailDomainsHandler struct{ cfg MeMailDomainsConfig }

type mailDomainRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MailboxCount int64  `json:"mailbox_count"`
	MailBytes    int64  `json:"mail_bytes"`
	Sent30d      int64  `json:"sent_30d"`
	Received30d  int64  `json:"received_30d"`
}

func (h *meMailDomainsHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()
	userID := claims.UserID

	domains, _, err := h.cfg.Domains.ListByUserID(ctx, userID, repository.ListOptions{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Per-domain mailbox count + storage, EXCLUDING the hidden system relay
	// (GH #1056 `system = 0`) so the numbers match the mailbox list the tenant
	// drills into. Best-effort: a stats hiccup shows zeros, not a 500.
	type agg struct{ count, bytes int64 }
	byDomainID := map[string]agg{}
	if rows, mErr := h.cfg.Mailboxes.ListByOwnerWithDomain(ctx, userID); mErr == nil {
		for i := range rows {
			m := &rows[i]
			if m.System {
				continue
			}
			a := byDomainID[m.DomainID]
			a.count++
			a.bytes += int64(m.LastUsageBytes)
			byDomainID[m.DomainID] = a
		}
	}

	// Sent/received over the last 30 days, owner-scoped, keyed by domain NAME
	// (DomainStatSample.Domain is the name). Only sent + received are summed;
	// delivered/failed are separate metrics carried in the same table.
	type traffic struct{ sent, received int64 }
	byName := map[string]traffic{}
	since := time.Now().Add(-30 * 24 * time.Hour)
	if samples, sErr := h.cfg.MailStats.DomainSeriesForUser(ctx, since, userID); sErr == nil {
		for _, s := range samples {
			t := byName[s.Domain]
			switch s.Metric {
			case "sent":
				t.sent += s.Value
			case "received":
				t.received += s.Value
			}
			byName[s.Domain] = t
		}
	}

	out := []mailDomainRow{}
	for i := range domains {
		d := &domains[i]
		if !d.EmailEnabled {
			continue
		}
		a := byDomainID[d.ID]
		t := byName[d.Name]
		out = append(out, mailDomainRow{
			ID:           d.ID,
			Name:         d.Name,
			MailboxCount: a.count,
			MailBytes:    a.bytes,
			Sent30d:      t.sent,
			Received30d:  t.received,
		})
	}

	// House list envelope (docs/CONVENTIONS.md) so panel-ui useListQuery reads
	// .data. A tenant has few mail domains, so this is unpaginated by design.
	c.JSON(http.StatusOK, gin.H{
		"data":      out,
		"total":     len(out),
		"page":      1,
		"page_size": len(out),
	})
}
