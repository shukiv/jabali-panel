package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// NotificationEventSettingsHandlerConfig wires the admin Notifications
// → Events tab. Repo is required; Log defaults to slog.Default().
type NotificationEventSettingsHandlerConfig struct {
	Repo repository.NotificationEventSettingRepository
	Log  *slog.Logger
}

// RegisterNotificationEventSettingsRoutes mounts:
//
//	GET    /admin/settings/notification-events
//	PATCH  /admin/settings/notification-events/:kind
func RegisterNotificationEventSettingsRoutes(g *gin.RouterGroup, cfg NotificationEventSettingsHandlerConfig) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &notificationEventSettingsHandler{cfg: cfg}
	admin := g.Group("/admin/settings/notification-events")
	admin.Use(middleware.RequireAdmin())
	admin.GET("", h.list)
	admin.PATCH("/:kind", h.update)
}

type notificationEventSettingsHandler struct {
	cfg NotificationEventSettingsHandlerConfig
}

type notificationEventDTO struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Enabled     bool   `json:"enabled"`
	DefaultOn   bool   `json:"default_on"`
}

func (h *notificationEventSettingsHandler) list(c *gin.Context) {
	rows, err := h.cfg.Repo.List(c.Request.Context())
	if err != nil {
		h.cfg.Log.Error("notification_event_settings list failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed"})
		return
	}
	enabledByKind := make(map[string]bool, len(rows))
	for _, r := range rows {
		enabledByKind[r.EventKind] = r.Enabled
	}

	out := make([]notificationEventDTO, 0, len(models.AllNotificationEventKinds))
	for _, meta := range models.AllNotificationEventKinds {
		enabled, has := enabledByKind[meta.Kind]
		if !has {
			// Row not yet seeded — show the meta default so the UI
			// reflects what would happen on next dispatch.
			enabled = meta.DefaultOn
		}
		out = append(out, notificationEventDTO{
			Kind:        meta.Kind,
			Label:       meta.Label,
			Description: meta.Description,
			Severity:    meta.Severity,
			Enabled:     enabled,
			DefaultOn:   meta.DefaultOn,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

type updateEventSettingRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *notificationEventSettingsHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	kind := c.Param("kind")
	if models.LookupNotificationEventKind(kind) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown event kind"})
		return
	}
	var req updateEventSettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "detail": err.Error()})
		return
	}
	if err := h.cfg.Repo.Set(c.Request.Context(), kind, req.Enabled); err != nil {
		h.cfg.Log.Error("notification_event_setting update failed", "kind", kind, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	h.cfg.Log.Info("event=audit kind=notification_event_setting_updated actor_id=" + claims.UserID + " event_kind=" + kind)
	c.Status(http.StatusNoContent)
}
