package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifsecret"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// NotificationsChannelsHandlerConfig wires the admin-facing
// /admin/notifications surface. Queue is required for the
// /channels/:id/test and /broadcast endpoints — the admin cannot test a
// channel without publishing on the same Redis stream the dispatcher
// drains.
type NotificationsChannelsHandlerConfig struct {
	Channels        repository.NotificationChannelRepository
	Webhooks        repository.WebhookEndpointRepository
	Queue           *notifications.Queue
	// Registry, when set, lets the "Send test" endpoint deliver synchronously
	// and report the real result instead of only enqueuing (GH #322).
	Registry        *notifications.Registry
	Log             *slog.Logger
	StrictRateLimit gin.HandlerFunc
	// SSOKey opens sealed channel secrets for the synchronous "Send test"
	// path (JAB-171). Nil = secrets used as stored (legacy plaintext).
	SSOKey *ssokey.Key
}

// redactChannelSecrets blanks every secret field on each channel's config so a
// GET never echoes a token/password (JAB-171). Operates on the passed rows,
// which are response-local copies loaded from the DB.
func redactChannelSecrets(rows []models.NotificationChannel) {
	for i := range rows {
		notifsecret.Redact(&rows[i].Config)
	}
}

// RegisterNotificationsChannelsRoutes mounts:
//
//   - GET    /admin/notifications/channels           list (paginated envelope)
//   - POST   /admin/notifications/channels           create
//   - PATCH  /admin/notifications/channels/:id       partial update
//   - DELETE /admin/notifications/channels/:id       delete
//   - POST   /admin/notifications/channels/:id/test  publish a synthetic envelope to one channel
//   - POST   /admin/notifications/broadcast          publish an envelope that fans out to every enabled channel
//
// The /test and /broadcast routes are wrapped with StrictRateLimit when
// supplied, matching the 5/min tier the shared rate limiter already
// exposes.
func RegisterNotificationsChannelsRoutes(g *gin.RouterGroup, cfg NotificationsChannelsHandlerConfig) {
	if cfg.Channels == nil {
		panic("api.RegisterNotificationsChannelsRoutes: cfg.Channels is nil")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &notificationsChannelsHandler{cfg: cfg}

	admin := g.Group("/admin/notifications", middleware.RequireAdmin())
	admin.GET("/channels", h.list)
	admin.POST("/channels", h.create)
	admin.PATCH("/channels/:id", h.update)
	admin.DELETE("/channels/:id", h.delete)

	// Per-admin token bucket — 5/min matches the plan. Shared across
	// /test + /broadcast because both paths publish to the same Redis
	// stream; a noisy admin spamming test-send is still abusive.
	perAdmin := newBroadcastLimit(time.Minute, 5).middleware()
	burst := []gin.HandlerFunc{perAdmin}
	if cfg.StrictRateLimit != nil {
		burst = append(burst, cfg.StrictRateLimit)
	}
	admin.POST("/channels/:id/test", append(burst, h.test)...)
	admin.POST("/broadcast", append(burst, h.broadcast)...)
}

type notificationsChannelsHandler struct {
	cfg NotificationsChannelsHandlerConfig
}

type channelListResponse struct {
	Data     []models.NotificationChannel `json:"data"`
	Total    int                          `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
}

func (h *notificationsChannelsHandler) list(c *gin.Context) {
	page, pageSize := parsePagination(c)
	opts := repository.ListOptions{
		Offset: (page - 1) * pageSize,
		Limit:  pageSize,
		Search: c.Query("search"),
		Sort:   c.Query("sort"),
		Order:  c.Query("order"),
	}
	rows, total, err := h.cfg.Channels.ListAll(c.Request.Context(), opts)
	if err != nil {
		h.cfg.Log.Error("list channels failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed"})
		return
	}
	redactChannelSecrets(rows)
	c.JSON(http.StatusOK, channelListResponse{Data: rows, Total: int(total), Page: page, PageSize: pageSize})
}

type channelCreateReq struct {
	Name    string                            `json:"name"`
	Kind    string                            `json:"kind"`
	Config  models.NotificationChannelConfig  `json:"config"`
	Enabled *bool                             `json:"enabled"`
}

func (h *notificationsChannelsHandler) create(c *gin.Context) {
	var req channelCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if err := validateChannelName(req.Name); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	if err := validateChannelKindAndConfig(req.Kind, req.Config); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row := &models.NotificationChannel{
		ID:      ids.NewULID(),
		Name:    req.Name,
		Kind:    req.Kind,
		Config:  req.Config,
		Enabled: enabled,
	}
	if err := h.cfg.Channels.Create(c.Request.Context(), row); err != nil {
		h.cfg.Log.Error("create channel failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	notifsecret.Redact(&row.Config)
	c.JSON(http.StatusCreated, row)
}

type channelUpdateReq struct {
	Name    *string                           `json:"name"`
	Config  *models.NotificationChannelConfig `json:"config"`
	Enabled *bool                             `json:"enabled"`
}

func (h *notificationsChannelsHandler) update(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	existing, err := h.cfg.Channels.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load failed"})
		return
	}
	var req channelUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if req.Name != nil {
		if err := validateChannelName(*req.Name); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		existing.Name = *req.Name
	}
	if req.Config != nil {
		// Write-only secrets: a secret left empty in the request keeps its
		// stored (sealed) value, so editing a channel after a redacted GET
		// doesn't wipe the token/password (JAB-171). Validate the MERGED
		// config so a secret-presence rule (e.g. webhook hmac_secret) still
		// passes on a preserved secret.
		merged := *req.Config
		notifsecret.PreserveEmptySecrets(&merged, existing.Config)
		if err := validateChannelKindAndConfig(existing.Kind, merged); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		existing.Config = merged
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := h.cfg.Channels.Update(ctx, existing); err != nil {
		h.cfg.Log.Error("update channel failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	notifsecret.Redact(&existing.Config)
	c.JSON(http.StatusOK, existing)
}

func (h *notificationsChannelsHandler) delete(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	if err := h.cfg.Channels.Delete(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		h.cfg.Log.Error("delete channel failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	// Best-effort: drop the webhook_endpoints row so a re-created
	// channel doesn't inherit stale failure counts.
	if h.cfg.Webhooks != nil {
		_ = h.cfg.Webhooks.Delete(ctx, id)
	}
	c.Status(http.StatusNoContent)
}

func (h *notificationsChannelsHandler) test(c *gin.Context) {
	if h.cfg.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dispatcher not initialised (Redis missing)"})
		return
	}
	id := c.Param("id")
	ctx := c.Request.Context()
	ch, err := h.cfg.Channels.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load failed"})
		return
	}
	env := notifications.Envelope{
		EventKind:  "notifications.channel.test",
		Severity:   models.NotificationSeverityInfo,
		Title:      fmt.Sprintf("Test notification for %s", ch.Name),
		Body:       "This is a synthetic envelope fired by the admin 'Send test' button. If you see it, the channel is wired up correctly.",
		Deeplink:   "/admin/notifications/channels/" + ch.ID,
		ChannelIDs: []string{ch.ID},
	}
	// Synchronous send (GH #322): deliver now and report the real result so a
	// misconfigured channel (e.g. SMTP auth failure) surfaces immediately
	// instead of failing silently in the async dispatcher.
	if h.cfg.SSOKey != nil {
		if oerr := notifsecret.Open(&ch.Config, *h.cfg.SSOKey); oerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "open channel secrets"})
			return
		}
	}
	if h.cfg.Registry != nil {
		sender, lerr := h.cfg.Registry.Lookup(ch.Kind)
		if lerr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no sender for channel kind " + ch.Kind})
			return
		}
		if !ch.Enabled {
			c.JSON(http.StatusConflict, gin.H{"error": "channel is disabled — enable it before testing"})
			return
		}
		if serr := sender.Send(ctx, *ch, env); serr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "delivery failed: " + serr.Error(), "channel_id": ch.ID})
			return
		}
		c.JSON(http.StatusOK, gin.H{"delivered": true, "channel_id": ch.ID})
		return
	}

	streamID, err := h.cfg.Queue.Publish(ctx, env)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "publish: " + err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": streamID, "channel_id": ch.ID})
}

type broadcastReq struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Deeplink string `json:"deeplink,omitempty"`
}

func (h *notificationsChannelsHandler) broadcast(c *gin.Context) {
	if h.cfg.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dispatcher not initialised (Redis missing)"})
		return
	}
	var req broadcastReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if req.Title == "" || len(req.Title) > 200 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "title required, <= 200 chars"})
		return
	}
	if len(req.Body) > 2000 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "body must be <= 2000 chars"})
		return
	}
	if !isKnownSeverity(req.Severity) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "severity must be info|warning|error|critical"})
		return
	}
	env := notifications.Envelope{
		EventKind: "notifications.broadcast",
		Severity:  req.Severity,
		Title:     req.Title,
		Body:      req.Body,
		Deeplink:  req.Deeplink,
	}
	streamID, err := h.cfg.Queue.Publish(c.Request.Context(), env)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "publish: " + err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": streamID})
}

// --- validation ---

var knownChannelKinds = map[string]struct{}{
	models.NotificationChannelKindEmail:   {},
	models.NotificationChannelKindSlack:   {},
	models.NotificationChannelKindDiscord: {},
	models.NotificationChannelKindNtfy:    {},
	models.NotificationChannelKindWebhook: {},
	models.NotificationChannelKindWebpush:  {},
	models.NotificationChannelKindSMS:      {},
	models.NotificationChannelKindTelegram: {},
}

var knownSeverities = map[string]struct{}{
	models.NotificationSeverityInfo:     {},
	models.NotificationSeverityWarning:  {},
	models.NotificationSeverityError:    {},
	models.NotificationSeverityCritical: {},
}

func isKnownSeverity(s string) bool {
	_, ok := knownSeverities[s]
	return ok
}

// ValidateChannelName / ValidateChannelKindConfig are exported so the
// `jabali notification channels create` CLI (#563) validates against the SAME
// rules as the REST create/update handlers — no drift.
func ValidateChannelName(name string) error { return validateChannelName(name) }
func ValidateChannelKindConfig(kind string, cfg models.NotificationChannelConfig) error {
	return validateChannelKindAndConfig(kind, cfg)
}

func validateChannelName(name string) error {
	if name == "" {
		return errors.New("name required")
	}
	if len(name) > 120 {
		return errors.New("name must be <= 120 chars")
	}
	return nil
}

func validateChannelKindAndConfig(kind string, cfg models.NotificationChannelConfig) error {
	if _, ok := knownChannelKinds[kind]; !ok {
		return fmt.Errorf("unknown channel kind %q (valid: email/slack/discord/ntfy/webhook/webpush/sms/telegram)", kind)
	}
	switch kind {
	case models.NotificationChannelKindEmail:
		if cfg.ToEmail == "" || cfg.FromEmail == "" {
			return errors.New("email channel requires to_email and from_email")
		}
		switch cfg.SMTPMode {
		case "", "local":
			// loopback Stalwart — no further fields required.
		case "smtp":
			if cfg.SMTPHost == "" {
				return errors.New("email channel: smtp_host required when smtp_mode=smtp")
			}
			if cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 {
				return errors.New("email channel: smtp_port must be 1–65535 when smtp_mode=smtp")
			}
			switch cfg.SMTPTLS {
			case "", "starttls", "tls", "none":
				// allowed
			default:
				return errors.New("email channel: smtp_tls must be starttls, tls, or none")
			}
		default:
			return fmt.Errorf("email channel: unknown smtp_mode %q (valid: local, smtp)", cfg.SMTPMode)
		}
	case models.NotificationChannelKindSlack, models.NotificationChannelKindDiscord:
		if err := validateHTTPURL(cfg.URL); err != nil {
			return fmt.Errorf("%s channel: %w", kind, err)
		}
	case models.NotificationChannelKindNtfy:
		if err := validateHTTPURL(cfg.URL); err != nil {
			return fmt.Errorf("ntfy channel: %w", err)
		}
	case models.NotificationChannelKindWebhook:
		if err := validateHTTPURL(cfg.URL); err != nil {
			return fmt.Errorf("webhook channel: %w", err)
		}
		if len(cfg.HMACSecret) < 16 {
			return errors.New("webhook channel: hmac_secret must be >= 16 chars")
		}
	case models.NotificationChannelKindWebpush:
		// No admin-configured fields — VAPID lives on server_settings,
		// subscriptions live on webpush_subscriptions.
	case models.NotificationChannelKindSMS:
		if err := validateHTTPURL(cfg.URL); err != nil {
			return fmt.Errorf("sms channel: %w", err)
		}
		if cfg.ToNumber == "" {
			return errors.New("sms channel: to_number required (E.164, e.g. +15551234567)")
		}
		// HMAC secret optional but recommended.
		if cfg.HMACSecret != "" && len(cfg.HMACSecret) < 16 {
			return errors.New("sms channel: hmac_secret must be >= 16 chars when set")
		}
	case models.NotificationChannelKindTelegram:
		if cfg.BotToken == "" {
			return errors.New("telegram channel: bot_token required (from @BotFather)")
		}
		if cfg.ChatID == "" {
			return errors.New("telegram channel: chat_id required (the chat/channel to post to)")
		}
	}
	return nil
}

func validateHTTPURL(raw string) error {
	if raw == "" {
		return errors.New("url required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must be http(s)")
	}
	if u.Host == "" {
		return errors.New("url must include host")
	}
	return nil
}

