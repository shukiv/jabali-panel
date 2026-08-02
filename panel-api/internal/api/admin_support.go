package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// AdminSupportHandlerConfig holds dependencies for the support endpoints.
type AdminSupportHandlerConfig struct {
	Agent agent.AgentInterface
}

// RegisterAdminSupportRoutes mounts the diagnostic endpoint under
// /admin/support. Admin-only. ADR-0064.
//
// POST /diagnostic — collect+redact+upload to enclosed; return URL+password.
// Operator forwards the credentials to the team via email — UI builds a
// mailto: link client-side, no server endpoint needed.
func RegisterAdminSupportRoutes(g *gin.RouterGroup, cfg AdminSupportHandlerConfig) {
	if cfg.Agent == nil {
		return
	}
	h := &adminSupportHandler{cfg: cfg}
	grp := g.Group("/admin/support")
	grp.Use(middleware.RequireAdmin())
	grp.POST("/diagnostic", h.diagnostic)
}

type adminSupportHandler struct{ cfg AdminSupportHandlerConfig }

// diagnostic asks the agent to collect host state, redact, encrypt and
// upload to enclosed. Returns URL + password for the operator to copy
// into a mail-client compose window. 120s timeout: journalctl for 10
// services + a few-MB enclosed POST over a slow link can stretch.
func (h *adminSupportHandler) diagnostic(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.diagnostic_report", nil)
	if err != nil {
		// GH #465 follow-up: "agent error" alone left a reporter with no way
		// to tell an egress problem from a dead agent — and Send Report IS the
		// diagnostic tool, so it must explain its own failures. Map the
		// agent's typed codes to stage-level, non-leaking error codes the UI
		// turns into actionable text. Detail still lands in the server log.
		code := "agent_error"
		var ae *agent.AgentError
		if errors.As(err, &ae) {
			switch ae.Code {
			case agentwire.CodeUpstreamUnavailable:
				code = "upload_unreachable"
			case agentwire.CodeDeadlineExceeded:
				code = "agent_timeout"
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "agent_timeout"
		}
		respondAgentErr(c, code, err)
		return
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	c.JSON(http.StatusOK, data)
}
