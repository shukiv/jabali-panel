package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// AdminServicesHandlerConfig holds dependencies for the service-control
// endpoints used by the M31 server-status page.
type AdminServicesHandlerConfig struct {
	Agent agent.AgentInterface
	Log   *slog.Logger
}

// RegisterAdminServicesRoutes mounts POST /admin/services/:name/{action}.
// RequireAdmin gates every route. Allowed actions: restart, start, stop,
// reload, enable, disable. Stop+disable are blocked for the panel
// self-destruct trio (jabali-panel, jabali-agent, mariadb) — those would
// brick the management plane mid-request.
func RegisterAdminServicesRoutes(g *gin.RouterGroup, cfg AdminServicesHandlerConfig) {
	if cfg.Agent == nil {
		return
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &adminServicesHandler{cfg: cfg}
	grp := g.Group("/admin/services")
	grp.Use(middleware.RequireAdmin())
	grp.POST("/:name/:action", h.action)
}

type adminServicesHandler struct{ cfg AdminServicesHandlerConfig }

var (
	servicesActionAllowlist = map[string]string{
		"restart": "service.restart",
		"start":   "service.start",
		"stop":    "service.stop",
		"reload":  "service.reload",
		"enable":  "service.enable",
		"disable": "service.disable",
	}
	// serviceNameRe is intentionally narrow — we re-validate panel-side
	// even though the agent does the same check. Only allowlisted units
	// (per ServiceListResponse) are valid; this regex eliminates
	// shell-injection-shaped strings before the request even hits the
	// agent.
	serviceNameRe = regexp.MustCompile(`^[a-zA-Z0-9._@-]+$`)

	// panelSelfDestructUnits are the units that, if stopped or disabled,
	// lock the operator out of the management plane mid-request:
	// jabali-panel hosts the very HTTP this request rode in on; the agent
	// is the only path back to systemctl on the host; mariadb backs
	// every panel session and DB-as-truth state; nginx is the reverse
	// proxy the panel is served through (stop -> 502); redis-server is a
	// hard dependency of jabali-panel (stop -> the panel process itself
	// dies — GH #746). jabali-kratos is the identity provider every panel
	// login and session refresh flows through — stopping it locks the
	// operator out of the management plane just as effectively (GH #746
	// follow-up). Reject these at the API layer so the agent stays a
	// dumb obedient executor and never has to know about product-UX
	// concerns. Keep this in sync with selfDestructUnits in the UI
	// (ServicesSummaryCard.tsx) and jabaliUnits in service_down.go.
	panelSelfDestructUnits = map[string]bool{
		"jabali-panel":  true,
		"jabali-agent":  true,
		"jabali-kratos": true,
		"mariadb":       true,
		"nginx":         true,
		"redis-server":  true,
	}
	panelSelfDestructActions = map[string]bool{
		"stop":    true,
		"disable": true,
	}
)

func (h *adminServicesHandler) action(c *gin.Context) {
	name := c.Param("name")
	action := c.Param("action")
	if !serviceNameRe.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_service_name"})
		return
	}
	cmd, ok := servicesActionAllowlist[action]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_action"})
		return
	}
	if panelSelfDestructUnits[name] && panelSelfDestructActions[action] {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "self_destruct_blocked",
			"details": "stop/disable on panel-critical units (jabali-panel, jabali-agent, jabali-kratos, mariadb, nginx, redis-server) would brick the management plane — use systemctl from the shell if you really need to",
		})
		return
	}

	actorID := ""
	if claims := ginctx.Claims(c); claims != nil {
		actorID = claims.UserID
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, cmd, map[string]any{"name": name})
	if err != nil {
		h.cfg.Log.Warn("event=audit kind=service_action_failed",
			"actor_id", actorID, "service", name, "action", action, "err", err.Error())
		respondAgentErr(c, "agent_error", err)
		return
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	h.cfg.Log.Info("event=audit kind=service_action",
		"actor_id", actorID, "service", name, "action", action)
	c.JSON(http.StatusOK, data)
}
