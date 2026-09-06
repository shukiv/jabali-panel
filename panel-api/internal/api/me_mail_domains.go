package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// me_mail_domains.go — GH #1387 Mail Domains summary (Mail → Mail Domains →
// accounts entry point).
//
// Owner-scoped. Lists ALL of the caller's domains (GH #1387 follow-up — not
// just mail-enabled ones) so the UI can show a mail Status and offer Enable/
// Disable per row; the toggle itself reuses POST/DELETE /domains/:id/email.
// The DB stats (domains via ListByUserID, mailbox
// count/storage via ListByOwnerWithDomain, 30-day sent/received via
// MailStatsRepository.DomainSeriesForUser) are always present. The current mail
// queue (v2) is a best-effort per-domain count derived from the server queue
// (agent mail.queue.list, filtered to the caller's own domains — only counts
// leave); on any agent hiccup the `queue` field is OMITTED so the UI shows
// "unknown" rather than a false 0. The account drill-down + mailbox-tab
// migration remain the operator's mail restructure and are out of scope here.

type MeMailDomainsConfig struct {
	Domains   repository.DomainRepository
	Mailboxes repository.MailboxRepository
	MailStats repository.MailStatsRepository
	// Agent backs the best-effort per-domain queue count. Optional: nil (or an
	// error) simply omits the queue field.
	Agent agent.AgentInterface
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
	// EmailEnabled is always true for listed rows (GH #1387, johnnyq 2026-09-01:
	// the list shows only mail-active domains). Kept on the wire so the UI can
	// still reason about mail state without a second fetch.
	EmailEnabled bool `json:"email_enabled"`
	// SSLState is the domain's computed cert state (off/pending/active_le/
	// self_signed/failed — DomainRepository.computeSSLState), surfaced for the
	// SSL column. Omitted when empty so the UI renders "Off".
	SSLState string `json:"ssl_state,omitempty"`
	// IsQuotaSuspended marks a bandwidth-suspended (mail-active) domain; the UI
	// badges it "Suspended" so the tenant sees why mail may be paused.
	IsQuotaSuspended bool `json:"is_quota_suspended"`
	// Queue is the count of queued messages touching this domain (as sender or
	// recipient). nil = unknown (agent unavailable) — omitted from JSON so the
	// UI shows "—" instead of a misleading 0.
	Queue *int64 `json:"queue,omitempty"`
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

	// GH #1387 (johnnyq, 2026-09-01): list ONLY domains where mail is active. A
	// mail-off domain is a Domains-page concern (enable mail there); this page is
	// the tenant's mailbox home and shows only domains that actually have mail.
	out := []mailDomainRow{}
	for i := range domains {
		d := &domains[i]
		if !d.EmailEnabled {
			continue
		}
		a := byDomainID[d.ID]
		t := byName[d.Name]
		out = append(out, mailDomainRow{
			ID:               d.ID,
			Name:             d.Name,
			MailboxCount:     a.count,
			MailBytes:        a.bytes,
			Sent30d:          t.sent,
			Received30d:      t.received,
			EmailEnabled:     d.EmailEnabled,
			SSLState:         d.SSLState,
			IsQuotaSuspended: d.IsQuotaSuspended,
		})
	}

	// v2: best-effort per-domain queue counts. On any agent failure every row's
	// queue stays nil (omitted) so the UI renders "unknown", not a false 0.
	h.attachQueueCounts(ctx, out)

	// House list envelope (docs/CONVENTIONS.md) so panel-ui useListQuery reads
	// .data. A tenant has few mail domains, so this is unpaginated by design.
	c.JSON(http.StatusOK, gin.H{
		"data":      out,
		"total":     len(out),
		"page":      1,
		"page_size": len(out),
	})
}

// attachQueueCounts sets each row's Queue to the number of server-queue messages
// touching that domain (as sender OR recipient), counted at most once per domain
// per message. The full server queue is pulled and filtered to the caller's own
// domains here — only per-domain counts ever reach the response. Best-effort: a
// nil/errored agent, a parse failure, or no rows leaves Queue nil (omitted).
func (h *meMailDomainsHandler) attachQueueCounts(ctx context.Context, rows []mailDomainRow) {
	if h.cfg.Agent == nil || len(rows) == 0 {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(qctx, "mail.queue.list", nil)
	if err != nil {
		return
	}
	var env struct {
		Data []mailQueueEntry `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	owned := make(map[string]bool, len(rows))
	for i := range rows {
		owned[strings.ToLower(rows[i].Name)] = true
	}
	counts := map[string]int64{}
	for _, e := range env.Data {
		touched := map[string]bool{}
		// returnPath may be angle-bracketed or the empty null-sender `<>`; strip
		// brackets and skip empties (a bounce is attributed only via recipients).
		if d := emailDomain(strings.Trim(e.From, "<>")); d != "" && owned[d] {
			touched[d] = true
		}
		for _, r := range e.Recipients {
			if d := emailDomain(strings.Trim(r, "<>")); d != "" && owned[d] {
				touched[d] = true
			}
		}
		for d := range touched {
			counts[d]++
		}
	}
	for i := range rows {
		n := counts[strings.ToLower(rows[i].Name)]
		rows[i].Queue = &n
	}
}
