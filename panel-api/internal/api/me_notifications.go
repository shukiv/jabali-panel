// Tenant-facing notification channels + routing (JAB-171 phase 3b).
//
//	GET    /me/notifications/channels          list the caller's own channels
//	POST   /me/notifications/channels          create an own channel (kind-gated)
//	PATCH  /me/notifications/channels/:id       update an own channel (write-only secrets)
//	DELETE /me/notifications/channels/:id       delete an own channel
//	POST   /me/notifications/channels/:id/test  synchronous test-send to an own channel
//	GET    /me/notifications/routes             list the caller's event→channel routes
//	POST   /me/notifications/routes             route one event kind to an own channel
//	DELETE /me/notifications/routes/:id         delete an own route
//
// SECURITY — this surface is gated behind ServerSettings.TenantNotificationsEnabled,
// which defaults OFF and has no admin toggle until phase 4. The gate fails CLOSED
// (unreadable settings → 403) because, unlike a UI module, exposing this early
// would let a tenant point an ntfy/discord channel at an internal address before
// the phase-4 SSRF guard exists. Every handler is ownership-scoped to
// claims.UserID and returns 404 (never 403) for another user's IDs so channel
// existence never leaks. Secrets are sealed at rest (notifsecret) and redacted on
// every response; edits are write-only (empty secret keeps the stored one).
//
// The tenant-creatable kind allowlist is admin-configurable
// (ServerSettings.TenantNotificationKinds, phase 4b): empty → the safe default
// set (ntfy/telegram/discord/webpush); webhook/slack/sms are admin opt-in and
// SSRF-guarded (phase 4a). email stays out until phase 4d ships verified-address
// plumbing.

package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifsecret"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// MeNotificationsConfig wires the per-user notification surface. Channels +
// Routes + ServerSettings are mandatory; without them the routes stay unmounted
// so a partial DI graph never half-serves the feature. Registry powers the
// synchronous test-send; nil → /test returns 503. SSOKey opens sealed secrets
// for the test send; nil → secrets used as stored (legacy plaintext).
type MeNotificationsConfig struct {
	Channels       repository.NotificationChannelRepository
	Routes         repository.UserNotificationRouteRepository
	ServerSettings repository.ServerSettingsRepository
	Registry       *notifications.Registry
	SSOKey         *ssokey.Key
	// Users resolves the caller's own account email for tenant email channels
	// (phase 4d). Required only for the email kind; nil → email create/update 503.
	Users repository.UserRepository
	Log   *slog.Logger
}

// RegisterMeNotificationsRoutes mounts the /me/notifications/* surface. Auth
// comes from the parent group; handlers resolve the caller from the session and
// enforce ownership + the master gate themselves.
func RegisterMeNotificationsRoutes(g *gin.RouterGroup, cfg MeNotificationsConfig) {
	if cfg.Channels == nil || cfg.Routes == nil || cfg.ServerSettings == nil {
		return
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &meNotificationsHandler{
		cfg:       cfg,
		testLimit: newBroadcastLimit(time.Minute, tenantTestSendPerMinute),
	}
	grp := g.Group("/me/notifications")
	grp.GET("/channels", h.listChannels)
	grp.POST("/channels", h.createChannel)
	grp.PATCH("/channels/:id", h.updateChannel)
	grp.DELETE("/channels/:id", h.deleteChannel)
	grp.POST("/channels/:id/test", h.testChannel)
	grp.GET("/routes", h.listRoutes)
	grp.POST("/routes", h.createRoute)
	grp.DELETE("/routes/:id", h.deleteRoute)
}

// Per-user anti-abuse limits (JAB-171 phase 4c). Consts, not admin-configurable
// — a conservative ceiling that a tenant can't tune. The quota bounds stored
// state; the test-send limit bounds tenant-triggerable outbound traffic (real
// events fire from system actions, not on tenant demand, so the test endpoint is
// the only tenant-controllable send).
const (
	maxTenantChannelsPerUser = 10
	tenantTestSendPerMinute  = 5
)

type meNotificationsHandler struct {
	cfg MeNotificationsConfig
	// testLimit is a per-user token bucket for the synchronous test-send,
	// reusing the admin broadcast limiter (keyed by claims.UserID).
	testLimit *perAdminBroadcastLimit
}

// loadSettings loads server settings and enforces the master gate. Fails
// CLOSED: a nil repo, a read error, or the flag being off all abort with 403.
// Security-sensitive — never fail open here. The returned settings are reused by
// createChannel for the kind allowlist so the gate + allowlist share one read.
func (h *meNotificationsHandler) loadSettings(c *gin.Context) (*models.ServerSettings, bool) {
	st, err := h.cfg.ServerSettings.Get(c.Request.Context())
	if err != nil || st == nil || !st.TenantNotificationsEnabled {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "tenant_notifications_disabled",
			"message": "per-user notification channels are not enabled on this server",
		})
		return nil, false
	}
	return st, true
}

// gateOpen is the master-gate check for handlers that don't need the settings.
func (h *meNotificationsHandler) gateOpen(c *gin.Context) bool {
	_, ok := h.loadSettings(c)
	return ok
}

// loadOwnedChannel fetches a channel and enforces tenant ownership. Any miss —
// not found, or owned by someone else / server-wide — returns a uniform 404 so
// a tenant can't probe for another user's channel IDs.
func (h *meNotificationsHandler) loadOwnedChannel(c *gin.Context, id, userID string) (*models.NotificationChannel, bool) {
	ch, err := h.cfg.Channels.FindByID(c.Request.Context(), id)
	if err != nil || ch == nil || ch.UserID == nil || *ch.UserID != userID {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, false
	}
	return ch, true
}

func (h *meNotificationsHandler) listChannels(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	rows, err := h.cfg.Channels.ListByUser(c.Request.Context(), claims.UserID)
	if err != nil {
		h.cfg.Log.Error("list own channels failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	redactChannelSecrets(rows)
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

type meChannelCreateReq struct {
	Name    string                           `json:"name"`
	Kind    string                           `json:"kind"`
	Config  models.NotificationChannelConfig `json:"config"`
	Enabled *bool                            `json:"enabled"`
}

func (h *meNotificationsHandler) createChannel(c *gin.Context) {
	st, ok := h.loadSettings(c)
	if !ok {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	var req meChannelCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if err := validateChannelName(req.Name); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	// Admin-configurable kind allowlist (empty settings → safe default set).
	if !st.TenantNotificationKinds.OrDefault().Allows(req.Kind) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":   "kind_not_allowed_for_tenant",
			"message": "channel kind " + req.Kind + " is not permitted for tenant channels on this server",
		})
		return
	}
	// Tenant email channels are forced to deliver only to the caller's own
	// account address over the local submission path — no arbitrary destination
	// (open relay) and no custom SMTP host (SSRF). Applied before validation so
	// the forced fields satisfy the email config rules.
	if req.Kind == models.NotificationChannelKindEmail {
		if !h.forceOwnEmailConfig(c, claims.UserID, &req.Config) {
			return
		}
	}
	if err := validateChannelKindAndConfig(req.Kind, req.Config); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	// Per-user channel quota — bound how many channels one tenant can create.
	existing, err := h.cfg.Channels.ListByUser(c.Request.Context(), claims.UserID)
	if err != nil {
		h.cfg.Log.Error("count own channels failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_failed"})
		return
	}
	if len(existing) >= maxTenantChannelsPerUser {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "too_many_channels",
			"message": fmt.Sprintf("channel limit reached (%d per user)", maxTenantChannelsPerUser),
			"max":     maxTenantChannelsPerUser,
		})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	uid := claims.UserID
	row := &models.NotificationChannel{
		ID:      ids.NewULID(),
		UserID:  &uid, // ownership is server-assigned, never from the body.
		Name:    req.Name,
		Kind:    req.Kind,
		Config:  req.Config,
		Enabled: enabled,
	}
	if err := h.cfg.Channels.Create(c.Request.Context(), row); err != nil {
		h.cfg.Log.Error("create own channel failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_failed"})
		return
	}
	notifsecret.Redact(&row.Config)
	c.JSON(http.StatusCreated, row)
}

type meChannelUpdateReq struct {
	Name    *string                           `json:"name"`
	Config  *models.NotificationChannelConfig `json:"config"`
	Enabled *bool                             `json:"enabled"`
}

func (h *meNotificationsHandler) updateChannel(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	existing, ok := h.loadOwnedChannel(c, c.Param("id"), claims.UserID)
	if !ok {
		return
	}
	var req meChannelUpdateReq
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
		// Write-only secrets: an empty secret in the request keeps the stored
		// (sealed) value, so editing after a redacted GET never wipes a token.
		// Validate the MERGED config so presence rules pass on preserved secrets.
		merged := *req.Config
		notifsecret.PreserveEmptySecrets(&merged, existing.Config)
		// Re-force own-address + local mode on every email edit so a tenant can't
		// PATCH to_email to an arbitrary destination after create (phase 4d).
		if existing.Kind == models.NotificationChannelKindEmail {
			if !h.forceOwnEmailConfig(c, claims.UserID, &merged) {
				return
			}
		}
		if err := validateChannelKindAndConfig(existing.Kind, merged); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		existing.Config = merged
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := h.cfg.Channels.Update(c.Request.Context(), existing); err != nil {
		h.cfg.Log.Error("update own channel failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed"})
		return
	}
	notifsecret.Redact(&existing.Config)
	c.JSON(http.StatusOK, existing)
}

func (h *meNotificationsHandler) deleteChannel(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	ch, ok := h.loadOwnedChannel(c, c.Param("id"), claims.UserID)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	// Drop the routes first (belt-and-suspenders alongside ON DELETE CASCADE) so
	// a dangling route never survives its channel.
	if err := h.cfg.Routes.DeleteByChannel(ctx, ch.ID); err != nil {
		h.cfg.Log.Error("delete routes for channel failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed"})
		return
	}
	if err := h.cfg.Channels.Delete(ctx, ch.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *meNotificationsHandler) testChannel(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	// Per-user test-send rate limit — the test endpoint is the only
	// tenant-triggerable outbound send, so cap it to blunt third-party spam.
	if h.testLimit != nil && !h.testLimit.allow(claims.UserID) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error":   "rate_limited",
			"message": fmt.Sprintf("test-send rate limit exceeded (%d/min)", tenantTestSendPerMinute),
		})
		return
	}
	if h.cfg.Registry == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dispatcher not initialised"})
		return
	}
	ch, ok := h.loadOwnedChannel(c, c.Param("id"), claims.UserID)
	if !ok {
		return
	}
	if !ch.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "channel is disabled — enable it before testing"})
		return
	}
	sender, lerr := h.cfg.Registry.Lookup(ch.Kind)
	if lerr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no sender for channel kind " + ch.Kind})
		return
	}
	// Open sealed secrets on this by-value copy before handing it to the sender;
	// the stored row stays sealed.
	if h.cfg.SSOKey != nil {
		if oerr := notifsecret.Open(&ch.Config, *h.cfg.SSOKey); oerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "open channel secrets"})
			return
		}
	}
	env := notifications.Envelope{
		EventKind: "notifications.channel.test",
		Severity:  models.NotificationSeverityInfo,
		Title:     fmt.Sprintf("Test notification for %s", ch.Name),
		Body:      "Synthetic envelope fired by the notifications 'Send test' button. If you see it, the channel is wired up correctly.",
		Deeplink:  "/me/notifications",
	}
	if serr := sender.Send(c.Request.Context(), *ch, env); serr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "delivery failed: " + serr.Error(), "channel_id": ch.ID})
		return
	}
	c.JSON(http.StatusOK, gin.H{"delivered": true, "channel_id": ch.ID})
}

// --- routes (event_kind → channel) ---

func (h *meNotificationsHandler) listRoutes(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	rows, err := h.cfg.Routes.ListByUser(c.Request.Context(), claims.UserID)
	if err != nil {
		h.cfg.Log.Error("list own routes failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

type meRouteCreateReq struct {
	EventKind string `json:"event_kind"`
	ChannelID string `json:"channel_id"`
}

func (h *meNotificationsHandler) createRoute(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	var req meRouteCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	req.EventKind = strings.TrimSpace(req.EventKind)
	if err := validateEventKind(req.EventKind); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	// The target channel must exist AND be owned by the caller — this is what
	// stops a tenant from routing their events at someone else's channel.
	if _, ok := h.loadOwnedChannel(c, req.ChannelID, claims.UserID); !ok {
		return
	}
	route := &models.UserNotificationRoute{
		ID:        ids.NewULID(),
		UserID:    claims.UserID,
		EventKind: req.EventKind,
		ChannelID: req.ChannelID,
	}
	if err := h.cfg.Routes.Create(c.Request.Context(), route); err != nil {
		// UNIQUE(user_id,event_kind,channel_id) → duplicate is a 409, not a 500.
		if isDuplicateKeyErr(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "route already exists"})
			return
		}
		h.cfg.Log.Error("create route failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_failed"})
		return
	}
	c.JSON(http.StatusCreated, route)
}

func (h *meNotificationsHandler) deleteRoute(c *gin.Context) {
	if !h.gateOpen(c) {
		return
	}
	claims, ok := requireBrowserAuth(c)
	if !ok {
		return
	}
	id := c.Param("id")
	// Ownership: confirm the route id belongs to the caller before deleting, so
	// a tenant can't delete another user's route by guessing its id. Uniform 404.
	routes, err := h.cfg.Routes.ListByUser(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed"})
		return
	}
	owned := false
	for i := range routes {
		if routes[i].ID == id {
			owned = true
			break
		}
	}
	if !owned {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err := h.cfg.Routes.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

// --- validation helpers ---

// forceOwnEmailConfig rewrites a tenant email channel's config so it can only
// deliver to the caller's OWN account address over the local submission path.
// This is the phase-4d safety property: no arbitrary destination (open relay)
// and no tenant-supplied SMTP host (SSRF). Returns false (and writes the
// response) when the user can't be resolved. Any body-supplied to_email /
// smtp_host / smtp_mode is discarded.
func (h *meNotificationsHandler) forceOwnEmailConfig(c *gin.Context, userID string, cfg *models.NotificationChannelConfig) bool {
	if h.cfg.Users == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email channels unavailable"})
		return false
	}
	u, err := h.cfg.Users.FindByID(c.Request.Context(), userID)
	if err != nil || u == nil || u.Email == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":   "no_account_email",
			"message": "your account has no email address to deliver to",
		})
		return false
	}
	// Preserve only the non-destination display/formatting fields; hard-set the
	// destination + transport to the safe own-account/local values.
	cfg.ToEmail = u.Email
	cfg.FromEmail = u.Email
	cfg.SMTPMode = "local"
	cfg.SMTPHost = ""
	cfg.SMTPPort = 0
	cfg.SMTPTLS = ""
	cfg.SMTPUsername = ""
	cfg.SMTPPassword = ""
	return true
}

// validateEventKind bounds the routed event kind. A stricter allowlist of
// routable kinds is a later phase; here we only guard shape.
func validateEventKind(k string) error {
	if k == "" {
		return errors.New("event_kind required")
	}
	if len(k) > 64 {
		return errors.New("event_kind must be <= 64 chars")
	}
	for _, r := range k {
		if !(r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return errors.New("event_kind may contain only letters, digits, '.', '_' and '-'")
		}
	}
	return nil
}

// isDuplicateKeyErr recognises a MySQL/MariaDB unique-constraint violation so a
// duplicate route maps to 409. Matches on the driver's 1062 code text, which
// survives GORM wrapping.
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "1062") || strings.Contains(strings.ToLower(s), "duplicate entry")
}
