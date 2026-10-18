package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/panelcert"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// AdminPanelCertificateHandlerConfig wires the dependencies for the
// M32 panel-cert REST surface. PanelCerts is the singleton repo;
// ServerSettings provides hostname + admin_email + public_ipv4 (the
// inputs to both the routability gate and the certbot invocation);
// Agent dispatches ssl.panel.issue; Routability is the gate
// implementation (separate so tests can stub it).
type AdminPanelCertificateHandlerConfig struct {
	PanelCerts     repository.PanelCertificateRepository
	ServerSettings repository.ServerSettingsRepository
	Agent          agent.AgentInterface
	Routability    *services.PanelCertRoutability
}

// RegisterAdminPanelCertificateRoutes mounts:
//
//	GET  /admin/panel-certificate          — current state + routability
//	POST /admin/panel-certificate/toggle   — flip use_le / staging flags
//	POST /admin/panel-certificate/issue    — force an immediate attempt;
//	                                         with Let's Encrypt off, the
//	                                         hostname's regenerates the
//	                                         self-signed cert (GH #2166)
//
// All gated by RequireAdmin. The reconciler picks up state changes
// on its own tick; toggle/issue endpoints can also synchronously
// fan out to the agent so the admin sees an immediate response.
func RegisterAdminPanelCertificateRoutes(g *gin.RouterGroup, cfg AdminPanelCertificateHandlerConfig) {
	if cfg.PanelCerts == nil || cfg.ServerSettings == nil {
		return
	}
	if cfg.Routability == nil {
		cfg.Routability = services.NewPanelCertRoutability()
	}
	h := &adminPanelCertHandler{cfg: cfg}
	grp := g.Group("/admin/panel-certificate")
	grp.Use(middleware.RequireAdmin())
	grp.GET("", h.get)
	grp.POST("/toggle", h.toggle)
	grp.POST("/issue", h.issue)
}

type adminPanelCertHandler struct {
	cfg AdminPanelCertificateHandlerConfig
}

// panelCertGetResponse layers the live routability check on top of the
// stored row so the UI never needs a separate request to render the
// "Use Let's Encrypt" toggle's enabled/disabled state.
type panelCertGetResponse struct {
	*models.PanelCertificate
	Routable       bool   `json:"routable"`
	RoutableReason string `json:"routable_reason,omitempty"`
	// Served describes the certificate file the kind serves, for the
	// Panel SSL tab (GH #2166): the row's status alone can't tell a
	// self-signed cert from a kept CA cert, nor give its names or real
	// expiry. ServedError says why it is absent.
	Served      *panelcert.Details `json:"served,omitempty"`
	ServedError string             `json:"served_error,omitempty"`
}

// servedPanelCert describes the certificate file behind row. Only a
// certificate is ever described (panelcert.Describe refuses anything else),
// so whatever the path holds, no key reaches the response.
func servedPanelCert(row *models.PanelCertificate) (*panelcert.Details, string) {
	path := row.CertPEMPath
	if path == "" {
		path = panelcert.DefaultCertPath
		if row.Kind == models.PanelCertKindMail {
			path = panelcert.DefaultMailCertPath
		}
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "No certificate at " + path
	case err != nil:
		return nil, "Can't read " + path
	}
	d, err := panelcert.Describe(data)
	if err != nil {
		return nil, path + " holds no valid certificate"
	}
	// JAB-179: the self-signed cert names the box's public IP.
	for i, n := range d.Names {
		if net.ParseIP(n) != nil {
			d.Names[i] = demoMaskIP(n)
		}
	}
	return d, ""
}

func (h *adminPanelCertHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	settings, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed", "details": err.Error()})
		return
	}
	if _, err := h.cfg.PanelCerts.EnsureDefault(ctx, settings.Hostname); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_load_failed", "details": err.Error()})
		return
	}
	rows, err := h.cfg.PanelCerts.ListAll(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_load_failed", "details": err.Error()})
		return
	}
	// Per-kind routability: the hostname row checks the panel
	// hostname; the mail row checks mail.<hostname> — so the UI shows
	// each cert's own reachability independently (ADR-0105).
	certs := make([]panelCertGetResponse, 0, len(rows))
	for _, row := range rows {
		res := panelCertGetResponse{PanelCertificate: row}
		// Display-only: local check (no external DNS per card open). The
		// authoritative public-resolvability gate runs at issue/reconcile
		// time; its reason surfaces via status + last_error.
		rr, _ := h.cfg.Routability.Check(ctx, row.Hostname, settings.PublicIPv4, false)
		res.Routable = rr.Routable
		res.RoutableReason = rr.Reason
		res.Served, res.ServedError = servedPanelCert(row)
		certs = append(certs, res)
	}
	c.JSON(http.StatusOK, gin.H{"certs": certs})
}

// panelCertToggleRequest is intentionally pointer-typed so callers
// can flip one flag without specifying the other. PATCH-style
// semantics on a singleton row.
type panelCertToggleRequest struct {
	UseLE   *bool `json:"use_le,omitempty"`
	Staging *bool `json:"staging,omitempty"`
}

func (h *adminPanelCertHandler) toggle(c *gin.Context) {
	var req panelCertToggleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "details": err.Error()})
		return
	}
	if req.UseLE == nil && req.Staging == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_fields_to_update"})
		return
	}
	ctx := c.Request.Context()
	settings, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
		return
	}
	row, err := h.cfg.PanelCerts.EnsureDefault(ctx, settings.Hostname)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_load_failed"})
		return
	}
	if req.UseLE != nil {
		row.UseLE = *req.UseLE
		// Turning off use_le doesn't tear down the existing LE cert
		// — we leave it in place until expiry, then the reconciler
		// replaces it with a self-signed one (panelcert.Check, GH
		// #2166). Avoids cert churn for an admin who toggles by accident.
		if !*req.UseLE && row.Status == models.PanelCertStatusPendingACMERetry {
			// Stop the retry loop when admin gives up. Status flips
			// to self_signed so the reconciler skips it; cert files
			// on disk are untouched.
			row.Status = models.PanelCertStatusSelfSigned
			row.NextRetryAt = nil
		}
	}
	if req.Staging != nil {
		row.Staging = *req.Staging
	}
	if err := h.cfg.PanelCerts.Upsert(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_save_failed", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, row)
}

// issue forces an immediate ssl.panel.issue dispatch, bypassing the
// reconciler's next_retry_at backoff. The status row is updated based
// on the agent response so the UI's TanStack Query refetch picks up
// the result without waiting on the reconciler tick.
func (h *adminPanelCertHandler) issue(c *gin.Context) {
	ctx := c.Request.Context()
	kind := c.Query("kind")
	if kind == "" {
		kind = models.PanelCertKindHostname
	}
	if kind != models.PanelCertKindHostname && kind != models.PanelCertKindMail {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_kind", "details": "kind must be hostname or mail"})
		return
	}
	settings, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
		return
	}
	if settings.Hostname == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_hostname", "details": "set Server Settings → General → Hostname first"})
		return
	}
	if _, err := h.cfg.PanelCerts.EnsureDefault(ctx, settings.Hostname); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_load_failed"})
		return
	}
	row, err := h.cfg.PanelCerts.GetByKind(ctx, kind)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_load_failed", "details": err.Error()})
		return
	}
	// GH #2166: with Let's Encrypt off the panel serves a self-signed
	// hostname cert, so the hostname's action regenerates it. The mail cert
	// stays a Let's Encrypt action: mail.<hostname> can't sit behind an HTTP
	// proxy, so it can be issued while the panel hostname is proxied.
	if kind == models.PanelCertKindHostname && !row.UseLE {
		h.selfSign(c, settings, row)
		return
	}
	if settings.AdminEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_admin_email", "details": "set Server Settings → General → Admin email first (required for Let's Encrypt registration)"})
		return
	}
	// Per-kind routability: mail checks mail.<hostname>. A failure
	// here parks ONLY this kind — the other cert is untouched.
	rr, _ := h.cfg.Routability.Check(ctx, row.Hostname, settings.PublicIPv4, kind == models.PanelCertKindMail)
	if !rr.Routable {
		_ = h.cfg.PanelCerts.MarkPendingRetryKind(ctx, kind, "not routable: "+rr.Reason, 3*time.Hour)
		c.JSON(http.StatusFailedDependency, gin.H{"error": "not_routable", "details": rr.Reason})
		return
	}

	row.Status = models.PanelCertStatusPendingACME
	if err := h.cfg.PanelCerts.Upsert(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_save_failed"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	raw, agentErr := h.cfg.Agent.Call(dispatchCtx, "ssl.panel.issue", map[string]any{
		"hostname":        row.Hostname,
		"extra_hostnames": []string{},
		"email":           settings.AdminEmail,
		"staging":         row.Staging,
		"kind":            kind,
		"cert_pem_path":   row.CertPEMPath,
	})
	if agentErr != nil {
		failReason := agentErr.Error()
		if kind == models.PanelCertKindMail {
			failReason = services.HumanizePanelCertError(row.Hostname, failReason)
		}
		_ = h.cfg.PanelCerts.MarkPendingRetryKind(ctx, kind, failReason, 3*time.Hour)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "issue_failed", "details": failReason})
		return
	}
	var resp struct {
		IssuedAt  string `json:"issued_at"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		_ = h.cfg.PanelCerts.MarkPendingRetryKind(ctx, kind, "failed to parse agent response: "+err.Error(), 3*time.Hour)
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse_failed"})
		return
	}
	issuedAt, err1 := time.Parse(time.RFC3339, resp.IssuedAt)
	expiresAt, err2 := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err1 != nil || err2 != nil {
		_ = h.cfg.PanelCerts.MarkPendingRetryKind(ctx, kind, "failed to parse agent timestamps", 3*time.Hour)
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_timestamp_parse_failed"})
		return
	}
	if err := h.cfg.PanelCerts.MarkIssuedKind(ctx, kind, issuedAt, expiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mark_issued_failed", "details": err.Error()})
		return
	}
	updated, _ := h.cfg.PanelCerts.GetByKind(ctx, kind)
	if updated == nil {
		updated = row
	}
	c.JSON(http.StatusOK, updated)
}

// panelSelfSignResponse answers the hostname's Issue action while Let's
// Encrypt is off. Detail is for the admin; ExpiresAt is the current cert's
// expiry when it is kept, for the UI to show in the admin's date format.
type panelSelfSignResponse struct {
	SelfSigned   bool   `json:"self_signed"`
	Regenerating bool   `json:"regenerating"`
	Detail       string `json:"detail"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// selfSign is the hostname's Issue action while Let's Encrypt is off (GH
// #2166). It regenerates the panel's self-signed cert instead of asking Let's
// Encrypt, so it needs neither public DNS nor an admin email. panelcert.Check
// decides, as it does for the reconciler and the agent: a live CA cert is
// kept until it expires, a current self-signed cert is left alone.
//
// A regenerate is dispatched detached and answered 202. ssl.panel.selfsign
// restarts jabali-panel to serve the new cert, and the restart waits for
// in-flight requests to drain, so a synchronous call would hold this request
// until the shutdown timeout. The agent reloads on its own context, so the
// panel stopping mid-call doesn't cut the reload short.
func (h *adminPanelCertHandler) selfSign(c *gin.Context, settings *models.ServerSettings, row *models.PanelCertificate) {
	certPath := row.CertPEMPath
	if certPath == "" {
		certPath = panelcert.DefaultCertPath
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		data = nil
	}
	v := panelcert.Check(data, settings.Hostname, time.Now())
	if v.Action == panelcert.Regenerate && h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	// The row loses the Let's Encrypt state a manual attempt left on it
	// (models.SelfSignedState). SetSelfSignedState leaves a row with use_le
	// on alone, so a toggle racing this can't be undone.
	if status, exp, changed := models.SelfSignedState(row, v); changed {
		if err := h.cfg.PanelCerts.SetSelfSignedState(c.Request.Context(), models.PanelCertKindHostname, status, exp); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "panel_cert_save_failed"})
			return
		}
	}

	host := settings.Hostname
	switch v.Action {
	case panelcert.UpToDate:
		c.JSON(http.StatusOK, panelSelfSignResponse{
			SelfSigned: true,
			Detail:     "The panel already serves a self-signed certificate for " + host + ".",
			ExpiresAt:  v.NotAfter.UTC().Format(time.RFC3339),
		})
	case panelcert.KeepCA:
		c.JSON(http.StatusOK, panelSelfSignResponse{
			SelfSigned: true,
			Detail:     "The panel serves a certificate issued by " + v.Issuer + ". It is kept until it expires, then replaced with a self-signed certificate.",
			ExpiresAt:  v.NotAfter.UTC().Format(time.RFC3339),
		})
	default:
		agentClient, ip := h.cfg.Agent, settings.PublicIPv4
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if _, err := agentClient.Call(ctx, "ssl.panel.selfsign", map[string]any{"hostname": host, "ip": ip}); err != nil {
				slog.Warn("panel self-signed cert regen failed", "hostname", host, "reason", v.Reason, "error", err)
			}
		}()
		c.JSON(http.StatusAccepted, panelSelfSignResponse{
			SelfSigned:   true,
			Regenerating: true,
			Detail:       "Regenerating the self-signed certificate for " + host + ". The panel restarts in a few seconds to serve it: reload the page, then accept the new certificate in your browser.",
		})
	}
}

// errPanelCertNoSettings is exported for the reconciler's logging path
// so it can distinguish a routability skip from a real failure.
var errPanelCertNoSettings = errors.New("server settings not initialised")
