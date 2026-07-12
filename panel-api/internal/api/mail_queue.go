package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// M47 Wave 2 (ADR-0103): admin mail-queue REST over the Wave-1 agent
// (stalwart-cli QueuedMessage). Canonical list envelope
// {data,total,page,page_size} (feedback_verify_wire_contract). The
// agent returns the full queue (small); panel-api owns the optional
// ?domain= scope + pagination so a per-domain view is one param.

const mailQueueCallTimeout = 15 * time.Second

type mailQueueEntry struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	Recipients []string `json:"recipients"`
	Size       int64    `json:"size"`
	Priority   int      `json:"priority"`
	NextRetry  string   `json:"next_retry"`
	FromIP     string   `json:"from_ip"`
}

func emailDomain(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i+1 < len(addr) {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// filterByDomain keeps entries whose MAIL FROM domain or any envelope
// recipient domain equals d (case-insensitive). Empty d = passthrough.
func filterByDomain(in []mailQueueEntry, d string) []mailQueueEntry {
	if d == "" {
		return in
	}
	d = strings.ToLower(d)
	out := make([]mailQueueEntry, 0, len(in))
	for _, e := range in {
		hit := emailDomain(e.From) == d
		for _, r := range e.Recipients {
			if emailDomain(r) == d {
				hit = true
				break
			}
		}
		if hit {
			out = append(out, e)
		}
	}
	return out
}

// paginate returns the page slice + clamped page/size. 1-based page;
// size default 50, cap 200.
func paginate(in []mailQueueEntry, page, size int) ([]mailQueueEntry, int, int) {
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * size
	if start >= len(in) {
		return []mailQueueEntry{}, page, size
	}
	end := start + size
	if end > len(in) {
		end = len(in)
	}
	return in[start:end], page, size
}

// RegisterAdminMailQueueRoutes mounts the admin mail-queue surface.
func RegisterAdminMailQueueRoutes(rg *gin.RouterGroup, cli agent.AgentInterface) {
	if cli == nil {
		return
	}
	g := rg.Group("/admin/mail", middleware.RequireAdmin())

	g.GET("/queue", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), mailQueueCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "mail.queue.list", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		var resp struct {
			Data  []mailQueueEntry `json:"data"`
			Total int              `json:"total"`
		}
		if jerr := json.Unmarshal(raw, &resp); jerr != nil {
			respondAgentErr(c, "agent_parse_failed", jerr)
			return
		}
		filtered := filterByDomain(resp.Data, c.Query("domain"))
		page, _ := strconv.Atoi(c.Query("page"))
		size, _ := strconv.Atoi(c.Query("page_size"))
		pageData, page, size := paginate(filtered, page, size)
		c.JSON(http.StatusOK, gin.H{
			"data": pageData, "total": len(filtered),
			"page": page, "page_size": size,
		})
	})

	g.POST("/queue/:id/retry", mailQueueIDAction(cli, "mail.queue.retry"))
	g.DELETE("/queue/:id", mailQueueIDAction(cli, "mail.queue.delete"))
}

func mailQueueIDAction(cli agent.AgentInterface, command string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing_id"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), mailQueueCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, command, map[string]any{"id": id})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	}
}
