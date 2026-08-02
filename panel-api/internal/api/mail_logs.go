// mail_logs.go — M6.5 Step 7 mail log viewer (pass-through).
//
// Wire contract: GET /mail/logs?from_date=&to_date=&sender=&recipient=&limit=&offset=
// Pass-through x:Trace/query + x:Trace/get via agent. User-scoped: only logs
// touching domains owned by the caller are returned.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type MailLogsHandlerConfig struct {
	Domains repository.DomainRepository
	Agent   agent.AgentInterface
}

type mailLogEntry struct {
	Timestamp string `json:"timestamp"`
	From      string `json:"from"`
	To        string `json:"to"`
	Size      int    `json:"size"`
	Status    string `json:"status"`             // GH #262: delivered/failed/queued
	QueueID   string `json:"queue_id,omitempty"` // GH #261
}

type mailLogsResponse struct {
	Data     []mailLogEntry `json:"data"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

type mailLogsHandler struct {
	cfg MailLogsHandlerConfig
}

func RegisterMailLogsRoutes(g *gin.RouterGroup, cfg MailLogsHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &mailLogsHandler{cfg: cfg}
	g.GET("/mail/logs", h.list)
	g.GET("/mail/logs/detail", h.detail)
}

func (h *mailLogsHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)

	// Build scope: user's domain names (admin sees all).
	var scope []string
	if !claims.IsAdmin {
		doms, _, err := h.cfg.Domains.ListByUserID(ctx, claims.UserID, repository.ListOptions{Limit: 500})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		for _, d := range doms {
			scope = append(scope, d.Name)
		}
		if len(scope) == 0 {
			c.JSON(http.StatusOK, mailLogsResponse{Data: []mailLogEntry{}, Total: 0, Page: 1, PageSize: 0})
			return
		}
	}

	params := map[string]any{
		"domain_names": scope,
		"limit":        parseIntDefault(c.Query("limit"), 50),
		"offset":       parseIntDefault(c.Query("offset"), 0),
	}
	if v := c.Query("from_date"); v != "" {
		params["from_date"] = v
	}
	if v := c.Query("to_date"); v != "" {
		params["to_date"] = v
	}
	if v := c.Query("sender"); v != "" {
		params["sender_prefix"] = v
	}
	if v := c.Query("recipient"); v != "" {
		params["recipient_prefix"] = v
	}

	if h.cfg.Agent == nil {
		c.JSON(http.StatusOK, mailLogsResponse{Data: []mailLogEntry{}, Total: 0, Page: 1, PageSize: 0})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(agentCtx, "mail.logs_query", params)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "logs_unavailable"})
		return
	}

	var agentResp struct {
		Entries []mailLogEntry `json:"entries"`
		Total   int            `json:"total"`
	}
	if err := json.Unmarshal(raw, &agentResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "logs_parse"})
		return
	}

	limit := parseIntDefault(c.Query("limit"), 50)
	c.JSON(http.StatusOK, mailLogsResponse{
		Data:     agentResp.Entries,
		Total:    agentResp.Total,
		Page:     parseIntDefault(c.Query("offset"), 0)/max1(limit) + 1,
		PageSize: limit,
	})
}

func parseIntDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return d
	}
	return n
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// detail returns the per-message delivery trail (GH #261) for one queueId,
// scoped exactly like list: a non-admin caller only sees a trail whose message
// involves one of their domains (the agent enforces this from domain_names).
func (h *mailLogsHandler) detail(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)

	queueID := c.Query("queue_id")
	if queueID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "queue_id_required"})
		return
	}

	var scope []string
	if !claims.IsAdmin {
		doms, _, err := h.cfg.Domains.ListByUserID(ctx, claims.UserID, repository.ListOptions{Limit: 500})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		for _, d := range doms {
			scope = append(scope, d.Name)
		}
		if len(scope) == 0 {
			c.JSON(http.StatusOK, gin.H{"queue_id": queueID, "events": []any{}})
			return
		}
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusOK, gin.H{"queue_id": queueID, "events": []any{}})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(agentCtx, "mail.logs_detail", map[string]any{
		"queue_id":     queueID,
		"domain_names": scope,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_call_failed"})
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}
