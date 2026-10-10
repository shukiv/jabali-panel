package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/smarthost"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/websitemail"
)

// GH #2056, ADR 0174 — where the sites' PHP mail() goes: the local mail
// server (today's behavior) or the operator's own smarthost, so a server
// without the mail module can still send its sites' contact-form mail.
//
// The smarthost login password is a credential for the operator's mail
// system: it is stored ONLY sealed with the sso key
// (server_settings.smarthost_password_enc), written ONLY through these
// endpoints, and NEVER returned; GET reports password_set yes/no. An empty
// password on PUT keeps the stored one. Switching to the smarthost tests it
// first, so a typo can't silently stop every site's mail, and the box is
// switched (agent mail.relay.apply) before anything is saved.

// WebsiteMailHandlerConfig wires the website-mail endpoints.
type WebsiteMailHandlerConfig struct {
	Repo repository.ServerSettingsRepository
	// SSOKey seals the smarthost password. Nil refuses to store or use one
	// (503): the panel must not hold it in plaintext.
	SSOKey   *ssokey.Key
	Recorder audit.Recorder
	// StrictRateLimit bounds PUT and the test: both make the panel dial the
	// host an admin typed.
	StrictRateLimit gin.HandlerFunc
	// Probe overrides smarthost.Probe in tests.
	Probe func(ctx context.Context, c smarthost.Config) error
	Log   *slog.Logger
	// Agent applies the setting on the box (mail.relay.apply) before it is
	// saved, so a save that couldn't switch the box changes nothing. Users,
	// Domains and Packages list the sites allowed to send through the
	// smarthost.
	Agent    agent.AgentInterface
	Users    repository.UserRepository
	Domains  repository.DomainRepository
	Packages repository.PackageRepository
}

// RegisterWebsiteMailRoutes mounts /admin/settings/website-mail under v1.
func RegisterWebsiteMailRoutes(g *gin.RouterGroup, cfg WebsiteMailHandlerConfig) {
	h := &websiteMailHandler{cfg: cfg}
	admin := g.Group("/admin/settings/website-mail")
	admin.Use(middleware.RequireAdmin())
	var limit []gin.HandlerFunc
	if cfg.StrictRateLimit != nil {
		limit = append(limit, cfg.StrictRateLimit)
	}
	admin.GET("", h.get)
	admin.PUT("", append(limit, h.put)...)
	admin.POST("/test", append(limit, h.test)...)
}

type websiteMailHandler struct{ cfg WebsiteMailHandlerConfig }

// websiteMailView is the GET/PUT response. It never carries the password.
type websiteMailView struct {
	Mode              string `json:"mode"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	TLS               string `json:"tls"`
	Username          string `json:"username"`
	PasswordSet       bool   `json:"password_set"`
	MailModuleEnabled bool   `json:"mail_module_enabled"`
	AllowedPorts      []int  `json:"allowed_ports"`
	// Senders is how many accounts may send through the smarthost (smarthost
	// mode only). Skipped names accounts the server left out on the last
	// save, with the reason.
	Senders *int     `json:"senders,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
}

// websiteMailRequest is the PUT and test body. An empty password keeps the
// stored one.
type websiteMailRequest struct {
	Mode     string `json:"mode"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
	Password string `json:"password"`
}

const (
	websiteMailProbeTimeout = 30 * time.Second
	websiteMailApplyTimeout = 60 * time.Second
)

func viewWebsiteMail(s *models.ServerSettings) websiteMailView {
	port, tlsMode := s.SmarthostPort, s.SmarthostTLS
	if port == 0 {
		port = 587
	}
	if tlsMode == "" {
		tlsMode = smarthost.TLSStartTLS
	}
	return websiteMailView{
		Mode:              models.EffectiveWebsiteMailMode(s),
		Host:              s.SmarthostHost,
		Port:              port,
		TLS:               tlsMode,
		Username:          s.SmarthostUsername,
		PasswordSet:       len(s.SmarthostPasswordEnc) > 0,
		MailModuleEnabled: s.MailEnabled,
		AllowedPorts:      smarthost.AllowedPorts,
	}
}

func (h *websiteMailHandler) settings(ctx context.Context) (*models.ServerSettings, error) {
	s, err := h.cfg.Repo.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		return &models.ServerSettings{ID: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	if s == nil {
		return &models.ServerSettings{ID: 1}, nil
	}
	return s, nil
}

func (h *websiteMailHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	s, err := h.settings(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_read_failed", "detail": "could not read server settings"})
		return
	}
	view := viewWebsiteMail(s)
	if view.Mode == models.WebsiteMailSmarthost {
		if senders, err := websitemail.Senders(ctx, h.deps()); err == nil {
			n := len(senders)
			view.Senders = &n
		}
	}
	c.JSON(http.StatusOK, view)
}

func (h *websiteMailHandler) deps() websitemail.Deps {
	return websitemail.Deps{Users: h.cfg.Users, Domains: h.cfg.Domains, Packages: h.cfg.Packages}
}

// apply switches the box to s (agent mail.relay.apply). With no agent wired
// (tests of the form alone) there is nothing to switch.
func (h *websiteMailHandler) apply(ctx context.Context, s *models.ServerSettings) (*websitemail.ApplyResponse, int, gin.H) {
	if h.cfg.Agent == nil {
		return nil, 0, nil
	}
	req, err := websitemail.Request(ctx, h.deps(), s, h.cfg.SSOKey)
	if errors.Is(err, websitemail.ErrNoKey) {
		return nil, http.StatusServiceUnavailable, ssoKeyMissing()
	}
	if errors.Is(err, websitemail.ErrDepsMissing) {
		return nil, http.StatusServiceUnavailable, gin.H{"error": "apply_failed", "detail": "the sender list is unavailable"}
	}
	if err != nil {
		return nil, http.StatusInternalServerError, gin.H{"error": "apply_failed", "detail": "could not build the website mail settings for the server"}
	}
	actx, cancel := context.WithTimeout(ctx, websiteMailApplyTimeout)
	defer cancel()
	resp, err := websitemail.Apply(actx, h.cfg.Agent, req)
	if err != nil {
		detail := "the server couldn't switch website mail"
		var ae *agent.AgentError
		if errors.As(err, &ae) && ae.Message != "" {
			detail += ": " + ae.Message
		}
		return nil, http.StatusBadGateway, gin.H{"error": "apply_failed", "detail": detail}
	}
	return resp, 0, nil
}

// resolve turns a request into the smarthost to use: the stored password
// fills in an empty one, and clearing the username drops the password. It
// returns the HTTP status and body to answer with when the request can't be
// used.
//
// The stored password is only ever sent to the host, as the username, it was
// saved with. Otherwise a Test or save pointed at another host with an empty
// password would hand the stored password to whoever runs that host (the
// panel logs in with it), so anyone holding an admin session could read it
// out. A new host or username needs the password typed again.
func (h *websiteMailHandler) resolve(req websiteMailRequest, s *models.ServerSettings) (smarthost.Config, int, gin.H) {
	cfg := smarthost.Config{
		Host:     strings.TrimSpace(req.Host),
		Port:     req.Port,
		TLS:      strings.TrimSpace(req.TLS),
		Username: strings.TrimSpace(req.Username),
		Password: req.Password,
		HeloName: s.Hostname,
	}
	if cfg.Username != "" && cfg.Password == "" && len(s.SmarthostPasswordEnc) > 0 {
		if !strings.EqualFold(cfg.Host, s.SmarthostHost) || cfg.Username != s.SmarthostUsername {
			return cfg, http.StatusUnprocessableEntity, gin.H{"error": "password_required", "detail": "enter the password again: the stored one is only used with the host and username it was saved for"}
		}
		if h.cfg.SSOKey == nil {
			return cfg, http.StatusServiceUnavailable, ssoKeyMissing()
		}
		pw, err := h.cfg.SSOKey.Open(s.SmarthostPasswordEnc)
		if err != nil {
			return cfg, http.StatusInternalServerError, gin.H{"error": "password_unreadable", "detail": "the stored smarthost password can't be read; enter it again"}
		}
		cfg.Password = string(pw)
	}
	if cfg.Username == "" {
		cfg.Password = ""
	}
	if err := smarthost.Validate(cfg); err != nil {
		return cfg, http.StatusUnprocessableEntity, gin.H{"error": "invalid_smarthost", "detail": err.Error()}
	}
	return cfg, 0, nil
}

func ssoKeyMissing() gin.H {
	return gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured, so the smarthost password can't be stored securely"}
}

func (h *websiteMailHandler) probe(ctx context.Context, cfg smarthost.Config) error {
	probe := h.cfg.Probe
	if probe == nil {
		probe = smarthost.Probe
	}
	pctx, cancel := context.WithTimeout(ctx, websiteMailProbeTimeout)
	defer cancel()
	return probe(pctx, cfg)
}

func probeFailed(err error) gin.H {
	body := gin.H{"error": "smarthost_test_failed", "detail": err.Error()}
	var se *smarthost.Error
	if errors.As(err, &se) {
		body["stage"] = se.Stage
	}
	return body
}

// test checks the smarthost in the body (the form as typed) without saving.
func (h *websiteMailHandler) test(c *gin.Context) {
	var req websiteMailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	s, err := h.settings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_read_failed", "detail": "could not read server settings"})
		return
	}
	cfg, status, body := h.resolve(req, s)
	if status != 0 {
		c.JSON(status, body)
		return
	}
	if err := h.probe(c.Request.Context(), cfg); err != nil {
		c.JSON(http.StatusUnprocessableEntity, probeFailed(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *websiteMailHandler) put(c *gin.Context) {
	var req websiteMailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode != models.WebsiteMailLocal && mode != models.WebsiteMailSmarthost {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_mode", "detail": `mode must be "local" or "smarthost"`})
		return
	}
	ctx := c.Request.Context()
	s, err := h.settings(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_read_failed", "detail": "could not read server settings"})
		return
	}
	actor := settingsEmailActor(c)

	next := *s
	next.WebsiteMailMode = mode
	if strings.TrimSpace(req.Host) == "" && mode == models.WebsiteMailLocal {
		// Back to the local mail server with the smarthost cleared.
		next.SmarthostHost, next.SmarthostPort, next.SmarthostTLS = "", 587, smarthost.TLSStartTLS
		next.SmarthostUsername, next.SmarthostPasswordEnc = "", nil
	} else {
		cfg, status, body := h.resolve(req, s)
		if status != 0 {
			c.JSON(status, body)
			return
		}
		newPassword := cfg.Username != "" && req.Password != ""
		if newPassword && h.cfg.SSOKey == nil {
			c.JSON(http.StatusServiceUnavailable, ssoKeyMissing())
			return
		}
		// Switching to (or editing) the smarthost in use: test it first.
		if mode == models.WebsiteMailSmarthost {
			if err := h.probe(ctx, cfg); err != nil {
				h.record(actor, mode, cfg.Host, models.AuditResultError)
				c.JSON(http.StatusUnprocessableEntity, probeFailed(err))
				return
			}
		}
		next.SmarthostHost, next.SmarthostPort, next.SmarthostTLS = cfg.Host, cfg.Port, cfg.TLS
		next.SmarthostUsername = cfg.Username
		switch {
		case cfg.Username == "":
			next.SmarthostPasswordEnc = nil
		case newPassword:
			sealed, err := h.cfg.SSOKey.Seal([]byte(cfg.Password))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "seal_failed", "detail": "could not encrypt the password"})
				return
			}
			next.SmarthostPasswordEnc = sealed
		}
	}

	// Switch the box first: a save it couldn't apply changes nothing.
	applied, status, body := h.apply(ctx, &next)
	if status != 0 {
		h.record(actor, mode, next.SmarthostHost, models.AuditResultError)
		c.JSON(status, body)
		return
	}
	if err := h.cfg.Repo.Upsert(ctx, &next); err != nil {
		h.record(actor, mode, next.SmarthostHost, models.AuditResultError)
		// Put the box back on what is saved; the reconciler retries if this
		// fails too.
		if _, st, _ := h.apply(ctx, s); st != 0 && h.cfg.Log != nil {
			h.cfg.Log.Warn("website mail: could not switch the server back after a failed save")
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_write_failed", "detail": "could not save the website mail settings"})
		return
	}
	h.record(actor, mode, next.SmarthostHost, models.AuditResultOK)
	if h.cfg.Log != nil {
		h.cfg.Log.Info("website mail settings saved", "mode", mode, "smarthost", next.SmarthostHost, "login", next.SmarthostUsername != "")
	}
	view := viewWebsiteMail(&next)
	if applied != nil && applied.Mode == models.WebsiteMailSmarthost {
		n := applied.Senders
		view.Senders = &n
		view.Skipped = applied.Skipped
	}
	c.JSON(http.StatusOK, view)
}

func (h *websiteMailHandler) record(actor, mode, host, result string) {
	if h.cfg.Recorder == nil {
		return
	}
	target := mode
	if host != "" {
		target = mode + ":" + auditTarget(host)
	}
	h.cfg.Recorder.Record(audit.APIMutation(actor, models.AuditActorAdmin, "", "settings.website_mail.update", "server_settings", target, result, "", ""))
}
