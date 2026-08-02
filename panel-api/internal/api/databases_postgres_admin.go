// Admin endpoints for the opt-in Postgres lifecycle (M37 Phase 4).
//
//	GET  /admin/databases/postgres/status   → pass-through to agent
//	POST /admin/databases/postgres/uninstall → destructive purge
//
// Toggle on/off lives on PATCH /admin/settings (postgres_enabled);
// these endpoints expose state the toggle alone can't reveal
// (installed but stopped, version drift) and the destructive
// uninstall path that purges /var/lib/postgresql.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

type PostgresAdminHandlerConfig struct {
	Agent agent.AgentInterface
	Log   *slog.Logger
}

func RegisterPostgresAdminRoutes(g *gin.RouterGroup, cfg PostgresAdminHandlerConfig) {
	h := &postgresAdminHandler{cfg: cfg}
	admin := g.Group("/admin/databases/postgres")
	admin.Use(middleware.RequireAdmin())
	admin.GET("/status", h.status)
	admin.POST("/uninstall", h.uninstall)
}

type postgresAdminHandler struct{ cfg PostgresAdminHandlerConfig }

type postgresStatusOut struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Version   string `json:"version,omitempty"`
}

func (h *postgresAdminHandler) status(c *gin.Context) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "db.postgres.status", map[string]any{})
	if err != nil {
		h.cfg.Log.Error("db.postgres.status failed", "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_call_failed"})
		return
	}
	var out postgresStatusOut
	if err := json.Unmarshal(raw, &out); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad_agent_response"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// uninstall is destructive — purges packages + removes
// /var/lib/postgresql + /etc/postgresql. UI must show a confirmation
// dialog before calling this. We intentionally don't expose this
// from the PATCH /admin/settings flow because flipping a toggle
// false should be reversible (just disable the service); only the
// explicit uninstall click drops user data.
func (h *postgresAdminHandler) uninstall(c *gin.Context) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "db.postgres.uninstall", map[string]any{})
	if err != nil {
		h.cfg.Log.Error("db.postgres.uninstall failed", "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_call_failed"})
		return
	}
	var out postgresStatusOut
	_ = json.Unmarshal(raw, &out)
	c.JSON(http.StatusOK, out)
}
