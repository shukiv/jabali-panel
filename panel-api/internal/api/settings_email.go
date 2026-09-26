// Settings → Email endpoint. Read-only view of the panel-primary domain
// for the admin UI's Email card (M6.4 / ADR-0048).
//
// Wire contract (verify against this file, per
// feedback_verify_wire_contract.md):
//
//	GET /api/v1/admin/settings/email
//
//	Two distinct response shapes, discriminated by HTTP status code
//	(NOT by field presence). Clients MUST switch on status.
//
//	200 OK — panel-primary domain row exists:
//	  {
//	    "primary_domain_name": "jabali-panel.local",
//	    "webmail_url":         "https://mail.jabali-panel.local/",
//	    "dkim_published":      true,                        // DkimPublicKey != nil && != ""
//	    "email_enabled_at":    "2026-04-22T18:00:00Z",      // RFC3339, or null
//	    "mail_hostname": {
//	      "effective": "mail.jabali-panel.local",           // the name mail is served on
//	      "applied":   null                                 // custom name in effect, or null = derived default
//	    },
//	    "switchover": null                                  // or the request in progress, see below
//	  }
//
//	webmail_url is built from mail_hostname.effective. The applied value is
//	server_settings.mail_hostname, written only by the reconciler once a
//	switchover to that name has converged (JAB-390). switchover is the
//	request an admin made (mail_hostname_switchover), null when there is
//	none or it was cancelled:
//	  {
//	    "desired":       "mx.example.net",
//	    "status":        "pending" | "issuing" | "failed" | "done",
//	    "last_error":    "",                                 // why the last attempt failed
//	    "next_retry_at": "2026-09-27T11:00:00Z",             // RFC3339, or null
//	    "updated_at":    "2026-09-27T10:50:00Z"
//	  }
//
//	PUT /api/v1/admin/settings/email/mail-hostname   {"mail_hostname": "mx.example.net"}
//	  202 {"switchover": {...}}  recorded; the reconciler applies it once the
//	      name points at this server and its certificate is issued.
//	  400 invalid_request | invalid_mail_hostname
//	  409 mail_hostname_not_ready | mail_hostname_refused | switchover_in_progress
//	  Switching back to the derived mail.<hostname> is a PUT of that name.
//
//	DELETE /api/v1/admin/settings/email/mail-hostname
//	  200 {"switchover": null}  a pending or failed request was withdrawn
//	  404 no_pending_switchover
//	  409 switchover_in_progress  an attempt is issuing
//
//	Refusals carry {"error": code, "detail": reason}; the reason is safe to
//	show. PUT and DELETE are audited (settings.mail_hostname.request /
//	.cancel), refused or not.
//
//	202 Accepted — row absent (install still converging, or pathological
//	operator SQL delete). Minimal shape, no null-filled fields:
//	  {
//	    "primary_domain_name": null,
//	    "status":              "initializing"
//	  }
//
// Absence is the designed behavior during the fresh-install convergence
// window; install.sh creates the row, the reconciler writes DKIM +
// zone records, and the UI progresses from "Initializing" to "Published"
// within ~30 seconds.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailhostops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// SettingsEmailHandlerConfig wires the handler to its repositories + logger.
type SettingsEmailHandlerConfig struct {
	Domains        repository.DomainRepository
	ServerSettings repository.ServerSettingsRepository
	// JAB-390 mail hostname switchover. With Switchover and PanelCerts set
	// the PUT/DELETE mail-hostname routes are mounted and GET reports the
	// request. WebDomainAliases and Recorder are optional.
	PanelCerts       repository.PanelCertificateRepository
	WebDomainAliases repository.WebDomainAliasRepository
	Switchover       repository.MailHostnameSwitchoverRepository
	Recorder         audit.Recorder
	// StrictRateLimit, when set, bounds the PUT/DELETE mail-hostname
	// routes. A new request resets the retry timer and starts an ACME
	// attempt on the next tick, so an unbounded loop of requests would
	// spend the Let's Encrypt failed-validation budget.
	StrictRateLimit gin.HandlerFunc
	Log             *slog.Logger
}

// RegisterSettingsEmailRoutes mounts GET /admin/settings/email under v1,
// and the mail-hostname setter when the switchover is wired. Must be called
// after v1's auth middleware is attached.
func RegisterSettingsEmailRoutes(g *gin.RouterGroup, cfg SettingsEmailHandlerConfig) {
	h := &settingsEmailHandler{cfg: cfg}
	admin := g.Group("/admin/settings/email")
	admin.Use(middleware.RequireAdmin())
	admin.GET("", h.get)
	if cfg.Switchover != nil && cfg.PanelCerts != nil {
		var limit []gin.HandlerFunc
		if cfg.StrictRateLimit != nil {
			limit = append(limit, cfg.StrictRateLimit)
		}
		admin.PUT("/mail-hostname", append(limit, h.requestMailHostname)...)
		admin.DELETE("/mail-hostname", append(limit, h.cancelMailHostname)...)
	}
}

type settingsEmailHandler struct {
	cfg SettingsEmailHandlerConfig
}

// settingsEmailOK is the 200 body shape.
type settingsEmailOK struct {
	PrimaryDomainName string                    `json:"primary_domain_name"`
	WebmailURL        string                    `json:"webmail_url"`
	DKIMPublished     bool                      `json:"dkim_published"`
	EmailEnabledAt    *time.Time                `json:"email_enabled_at"`
	MailHostname      settingsEmailMailHostname `json:"mail_hostname"`
	Switchover        *settingsEmailSwitchover  `json:"switchover"`
}

type settingsEmailMailHostname struct {
	Effective string  `json:"effective"`
	Applied   *string `json:"applied"`
}

// settingsEmailSwitchover is the JAB-390 switchover request's progress.
type settingsEmailSwitchover struct {
	Desired     *string    `json:"desired"`
	Status      string     `json:"status"`
	LastError   string     `json:"last_error"`
	NextRetryAt *time.Time `json:"next_retry_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func switchoverView(sw *models.MailHostnameSwitchover) *settingsEmailSwitchover {
	if sw == nil || sw.Status == models.MailHostnameSwitchoverIdle || sw.Desired == nil {
		return nil
	}
	return &settingsEmailSwitchover{
		Desired:     sw.Desired,
		Status:      sw.Status,
		LastError:   sw.LastError,
		NextRetryAt: sw.NextRetryAt,
		UpdatedAt:   sw.UpdatedAt,
	}
}

// settingsEmailInitializing is the 202 body shape. Deliberately separate
// struct so the client can discriminate on HTTP status without parsing
// against a union with nullable-everywhere fields.
type settingsEmailInitializing struct {
	PrimaryDomainName *string `json:"primary_domain_name"` // always nil; present for schema sanity
	Status            string  `json:"status"`              // always "initializing"
}

func (h *settingsEmailHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	d, err := h.cfg.Domains.FindPanelPrimary(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrPanelPrimaryNotFound) {
			c.JSON(http.StatusAccepted, settingsEmailInitializing{
				PrimaryDomainName: nil,
				Status:            "initializing",
			})
			return
		}
		h.cfg.Log.Error("find panel primary", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// JAB-390: server_settings.mail_hostname is the APPLIED mail hostname.
	// No settings row means none is applied. Any other read failure is a
	// 500, not a guess: the derived default could name a host the panel no
	// longer serves mail on.
	var stored *string
	srv, err := h.cfg.ServerSettings.Get(ctx)
	switch {
	case err == nil:
		stored = srv.MailHostname
	case errors.Is(err, repository.ErrNotFound):
	default:
		h.cfg.Log.Error("read server settings", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	mailHost := settingsEmailMailHostname{Effective: models.EffectiveMailHostname(stored, d.Name)}
	if applied, ok := models.AppliedMailHostname(stored); ok {
		mailHost.Applied = &applied
	}

	var switchover *settingsEmailSwitchover
	if h.cfg.Switchover != nil {
		sw, err := h.cfg.Switchover.Get(ctx)
		switch {
		case err == nil:
			switchover = switchoverView(sw)
		case errors.Is(err, repository.ErrNotFound):
		default:
			h.cfg.Log.Error("read mail hostname switchover", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	}

	dkimPublished := d.DkimPublicKey != nil && *d.DkimPublicKey != ""
	c.JSON(http.StatusOK, settingsEmailOK{
		PrimaryDomainName: d.Name,
		WebmailURL:        "https://" + mailHost.Effective + "/",
		DKIMPublished:     dkimPublished,
		EmailEnabledAt:    d.EmailEnabledAt,
		MailHostname:      mailHost,
		Switchover:        switchover,
	})
}

func (h *settingsEmailHandler) requestDeps() mailhostops.RequestDeps {
	deps := mailhostops.RequestDeps{
		Settings:   h.cfg.ServerSettings,
		PanelCerts: h.cfg.PanelCerts,
		Domains:    h.cfg.Domains,
		Switchover: h.cfg.Switchover,
	}
	if h.cfg.WebDomainAliases != nil {
		deps.Aliases = h.cfg.WebDomainAliases
	}
	return deps
}

// requestMailHostname records a JAB-390 switchover request.
func (h *settingsEmailHandler) requestMailHostname(c *gin.Context) {
	var req struct {
		MailHostname string `json:"mail_hostname"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": "body must be {\"mail_hostname\": \"<host>\"}"})
		return
	}
	actor := settingsEmailActor(c)
	name, err := mailhostops.Request(c.Request.Context(), h.requestDeps(), req.MailHostname, "admin:"+actor)
	target := name
	if target == "" {
		target = auditTarget(req.MailHostname)
	}
	var status int
	var code string
	switch {
	case err == nil:
	case errors.Is(err, mailhostops.ErrInvalidName):
		status, code = http.StatusBadRequest, "invalid_mail_hostname"
	case errors.Is(err, mailhostops.ErrNotReady):
		status, code = http.StatusConflict, "mail_hostname_not_ready"
	case errors.Is(err, mailhostops.ErrNameRefused):
		status, code = http.StatusConflict, "mail_hostname_refused"
	case errors.Is(err, repository.ErrSwitchoverInFlight):
		status, code = http.StatusConflict, "switchover_in_progress"
	default:
		h.cfg.Log.Error("request mail hostname switchover", "err", err)
		h.record(actor, "settings.mail_hostname.request", target, models.AuditResultError)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err != nil {
		h.record(actor, "settings.mail_hostname.request", target, models.AuditResultDenied)
		c.JSON(status, gin.H{"error": code, "detail": refusalDetail(err)})
		return
	}
	h.record(actor, "settings.mail_hostname.request", target, models.AuditResultOK)
	c.JSON(http.StatusAccepted, gin.H{"switchover": settingsEmailSwitchover{
		Desired:   &name,
		Status:    models.MailHostnameSwitchoverPending,
		UpdatedAt: time.Now().UTC(),
	}})
}

// cancelMailHostname withdraws a pending or failed switchover request.
func (h *settingsEmailHandler) cancelMailHostname(c *gin.Context) {
	actor := settingsEmailActor(c)
	err := mailhostops.Cancel(c.Request.Context(), h.requestDeps())
	switch {
	case err == nil:
		h.record(actor, "settings.mail_hostname.cancel", "", models.AuditResultOK)
		c.JSON(http.StatusOK, gin.H{"switchover": nil})
	case errors.Is(err, repository.ErrNotFound):
		h.record(actor, "settings.mail_hostname.cancel", "", models.AuditResultDenied)
		c.JSON(http.StatusNotFound, gin.H{"error": "no_pending_switchover", "detail": "there is no pending mail hostname change to cancel"})
	case errors.Is(err, repository.ErrSwitchoverInFlight):
		h.record(actor, "settings.mail_hostname.cancel", "", models.AuditResultDenied)
		c.JSON(http.StatusConflict, gin.H{"error": "switchover_in_progress", "detail": "the certificate for the new mail hostname is being issued; try again in a few minutes"})
	default:
		h.cfg.Log.Error("cancel mail hostname switchover", "err", err)
		h.record(actor, "settings.mail_hostname.cancel", "", models.AuditResultError)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}

func (h *settingsEmailHandler) record(actor, action, target, result string) {
	if h.cfg.Recorder == nil {
		return
	}
	h.cfg.Recorder.Record(audit.APIMutation(actor, models.AuditActorAdmin, "", action, "server_settings", target, result, "", ""))
}

func settingsEmailActor(c *gin.Context) string {
	if claims := ginctx.Claims(c); claims != nil {
		return claims.UserID
	}
	return ""
}

// refusalDetail is the admin-facing reason for a refused request.
func refusalDetail(err error) string {
	if errors.Is(err, repository.ErrSwitchoverInFlight) {
		return "a mail hostname change is being applied; wait for it to finish"
	}
	return err.Error()
}

// auditTarget bounds a raw, possibly invalid input for the audit log.
func auditTarget(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 253 {
		s = s[:253]
	}
	return s
}
