package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// NotificationsWebPushHandlerConfig wires the browser-facing Web Push
// enrolment endpoints. ServerSettings exposes the VAPID public key so
// the browser can call pushManager.subscribe; Subs persists the
// resulting endpoint.
type NotificationsWebPushHandlerConfig struct {
	ServerSettings repository.ServerSettingsRepository
	Subs           repository.WebPushSubscriptionRepository
	// Channels is optional. When non-nil, the handler ensures one
	// enabled webpush NotificationChannel row exists after a successful
	// subscribe — without it, the dispatcher's resolveTargets returns
	// no target for the webpush sender and the OS-level push never
	// fires. Cheap idempotent guard (FindEnabledByKind first, Create
	// only on empty).
	Channels repository.NotificationChannelRepository
	Log      *slog.Logger
}

// RegisterNotificationsWebPushRoutes mounts:
//
//   - GET    /notifications/webpush/vapid-public-key
//   - POST   /notifications/webpush/subscribe     — upsert on endpoint uniqueness
//   - DELETE /notifications/webpush/subscribe     — unsubscribe current browser
func RegisterNotificationsWebPushRoutes(g *gin.RouterGroup, cfg NotificationsWebPushHandlerConfig) {
	if cfg.Subs == nil || cfg.ServerSettings == nil {
		panic("api.RegisterNotificationsWebPushRoutes: Subs and ServerSettings required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &webpushHandler{cfg: cfg}
	g.GET("/notifications/webpush/vapid-public-key", h.vapidKey)
	g.POST("/notifications/webpush/subscribe", h.subscribe)
	g.DELETE("/notifications/webpush/subscribe", h.unsubscribe)
}

type webpushHandler struct {
	cfg NotificationsWebPushHandlerConfig
}

func (h *webpushHandler) vapidKey(c *gin.Context) {
	settings, err := h.cfg.ServerSettings.Get(c.Request.Context())
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_settings load failed"})
		return
	}
	if settings == nil || settings.VAPIDPublicKey == nil || *settings.VAPIDPublicKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "VAPID keypair not yet seeded"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"public_key": *settings.VAPIDPublicKey})
}

type webpushSubscribeReq struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	UserAgent string `json:"user_agent,omitempty"`
}

func (h *webpushHandler) subscribe(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var req webpushSubscribeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if err := validateSubscribeReq(req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	// Upsert: the endpoint column has a UNIQUE constraint, so hand a
	// new row to Upsert and let the repo merge p256dh/auth/last_used_at
	// onto an existing row if present.
	sub := &models.WebPushSubscription{
		ID:        ids.NewULID(),
		UserID:    claims.UserID,
		Endpoint:  req.Endpoint,
		P256dh:    req.Keys.P256dh,
		Auth:      req.Keys.Auth,
		UserAgent: req.UserAgent,
	}
	if err := h.cfg.Subs.Upsert(ctx, sub); err != nil {
		h.cfg.Log.Error("webpush subscribe failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "subscribe failed"})
		return
	}
	// Ensure an enabled webpush channel exists so the dispatcher
	// actually fans out to the WebPush sender. Idempotent: skips
	// when one is already enabled. Failures are logged and ignored
	// — subscription persisted is the user-visible outcome.
	if h.cfg.Channels != nil {
		existing, lookupErr := h.cfg.Channels.FindEnabledByKind(ctx, models.NotificationChannelKindWebpush)
		switch {
		case lookupErr != nil:
			h.cfg.Log.Warn("webpush subscribe: channel lookup failed", "err", lookupErr)
		case len(existing) == 0:
			ch := &models.NotificationChannel{
				ID:      ids.NewULID(),
				Name:    "Web Push",
				Kind:    models.NotificationChannelKindWebpush,
				Enabled: true,
			}
			if cErr := h.cfg.Channels.Create(ctx, ch); cErr != nil {
				h.cfg.Log.Warn("webpush subscribe: auto-create channel failed", "err", cErr)
			} else {
				h.cfg.Log.Info("webpush subscribe: auto-created webpush channel", "channel_id", ch.ID)
			}
		}
	}
	// The upserted row may have a different ID if the endpoint existed;
	// re-fetch so the SPA displays the authoritative value.
	persisted, err := h.cfg.Subs.FindByEndpoint(ctx, req.Endpoint)
	if err != nil {
		// Soft fail — subscription is persisted, we just can't echo it.
		c.JSON(http.StatusCreated, sub)
		return
	}
	c.JSON(http.StatusCreated, persisted)
}

type webpushUnsubscribeReq struct {
	Endpoint string `json:"endpoint"`
}

func (h *webpushHandler) unsubscribe(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var req webpushUnsubscribeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if req.Endpoint == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "endpoint required"})
		return
	}
	ctx := c.Request.Context()
	// Only let the caller unsubscribe a row they own. FindByEndpoint +
	// compare UserID (admins can unsubscribe any).
	existing, err := h.cfg.Subs.FindByEndpoint(ctx, req.Endpoint)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.Status(http.StatusNoContent)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load failed"})
		return
	}
	if existing.UserID != claims.UserID && !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your subscription"})
		return
	}
	if err := h.cfg.Subs.DeleteByEndpoint(ctx, req.Endpoint); err != nil {
		h.cfg.Log.Error("webpush unsubscribe failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unsubscribe failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

func validateSubscribeReq(r webpushSubscribeReq) error {
	if r.Endpoint == "" {
		return errors.New("endpoint required")
	}
	if len(r.Endpoint) > 500 {
		return errors.New("endpoint must be <= 500 chars")
	}
	if r.Keys.P256dh == "" {
		return errors.New("keys.p256dh required")
	}
	if r.Keys.Auth == "" {
		return errors.New("keys.auth required")
	}
	if len(r.Keys.P256dh) > 200 {
		return errors.New("keys.p256dh must be <= 200 chars")
	}
	if len(r.Keys.Auth) > 50 {
		return errors.New("keys.auth must be <= 50 chars")
	}
	if len(r.UserAgent) > 300 {
		return errors.New("user_agent must be <= 300 chars")
	}
	return nil
}
