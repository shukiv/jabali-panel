package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// AdminProcessesHandlerConfig wires the kill endpoint used by the
// Server Status page.
type AdminProcessesHandlerConfig struct {
	Agent agent.AgentInterface
	Log   *slog.Logger
}

// RegisterAdminProcessesRoutes mounts POST /admin/processes/:pid/kill.
// RequireAdmin gates the route. The agent owns the actual signal +
// denylist enforcement; panel-api just validates the pid is numeric,
// audit-logs, and forwards.
func RegisterAdminProcessesRoutes(g *gin.RouterGroup, cfg AdminProcessesHandlerConfig) {
	if cfg.Agent == nil {
		return
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &adminProcessesHandler{cfg: cfg}
	grp := g.Group("/admin/processes")
	grp.Use(middleware.RequireAdmin())
	grp.POST("/:pid/kill", h.kill)
}

type adminProcessesHandler struct{ cfg AdminProcessesHandlerConfig }

type killRequest struct {
	Force bool `json:"force"`
}

func (h *adminProcessesHandler) kill(c *gin.Context) {
	pid, err := strconv.Atoi(c.Param("pid"))
	if err != nil || pid < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_pid"})
		return
	}
	var req killRequest
	_ = c.ShouldBindJSON(&req) // body is optional

	actorID := ""
	if claims := ginctx.Claims(c); claims != nil {
		actorID = claims.UserID
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.kill_process", map[string]any{
		"pid":   pid,
		"force": req.Force,
	})
	if err != nil {
		h.cfg.Log.Warn("event=audit kind=process_kill_failed",
			"actor_id", actorID, "pid", pid, "force", req.Force, "err", err.Error())
		respondAgentErr(c, "agent_error", err)
		return
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	h.cfg.Log.Info("event=audit kind=process_kill",
		"actor_id", actorID, "pid", pid, "force", req.Force)
	c.JSON(http.StatusOK, data)
}
