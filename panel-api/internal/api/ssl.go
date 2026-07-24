package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// SSLScheduler is a minimal interface for scheduling domain reconciliation.
type SSLScheduler interface {
	Schedule(domainID string)
}

// SSLHandlerConfig bundles dependencies for SSL certificate handlers.
type SSLHandlerConfig struct {
	Domains        repository.DomainRepository
	SSLCerts       repository.SSLCertificateRepository
	SharedCerts    repository.SharedCertificateRepository
	PanelCerts     repository.PanelCertificateRepository
	MailCerts      repository.MailCertificateRepository
	ServerSettings repository.ServerSettingsRepository
	Reconciler     SSLScheduler
	Config         *config.Config
	Agent          agent.AgentInterface
}

// sslHandler provides HTTP handlers for SSL certificate endpoints.
type sslHandler struct {
	cfg SSLHandlerConfig
}

// SSLResponse represents the SSL certificate status for a domain.
type SSLResponse struct {
	Status        string     `json:"status"`
	IssuedAt      *time.Time `json:"issued_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RenewalCount  int        `json:"renewal_count"`
	LastRenewedAt *time.Time `json:"last_renewed_at,omitempty"`
	LastError     *string    `json:"last_error,omitempty"`
	Staging       bool       `json:"staging"`
	CertPath      *string    `json:"cert_path,omitempty"`
	KeyPath       *string    `json:"key_path,omitempty"`
}

// newSSLHandler creates a new SSL handler.
func newSSLHandler(cfg SSLHandlerConfig) *sslHandler {
	return &sslHandler{cfg: cfg}
}

// RegisterSSLRoutes wires SSL endpoints into the given router.
func RegisterSSLRoutes(g *gin.RouterGroup, cfg SSLHandlerConfig) {
	h := newSSLHandler(cfg)

	domains := g.Group("/domains/:id")
	{
		domains.GET("/ssl", h.getSSL)
		domains.POST("/ssl", h.enableSSL)
		domains.DELETE("/ssl", h.disableSSL)
		domains.POST("/ssl/renew", h.renewSSL)
		domains.PUT("/ssl/custom", h.installCustomSSL)
		domains.POST("/ssl/shared", h.attachSharedCert)
		domains.DELETE("/ssl/shared", h.detachSharedCert)
		domains.POST("/ssl/retry", h.retrySSL)
	}

	// List endpoints
	g.GET("/admin/ssl-certificates", middleware.RequireAdmin(), h.listAllSSL)
	g.GET("/ssl-certificates", h.listUserSSL)

	// Shared certificates (JAB-170): admin upload/list/delete of a
	// wildcard/multi-SAN cert served from many domains.
	g.POST("/admin/certificates/shared", middleware.RequireAdmin(), h.uploadSharedCert)
	g.GET("/admin/certificates/shared", middleware.RequireAdmin(), h.listSharedCerts)
	g.PUT("/admin/certificates/shared/:id", middleware.RequireAdmin(), h.replaceSharedCert)
	g.DELETE("/admin/certificates/shared/:id", middleware.RequireAdmin(), h.deleteSharedCert)
}

// getSSL retrieves the current SSL certificate status for a domain.
// GET /api/v1/domains/:id/ssl
func (h *sslHandler) getSSL(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	domain := h.loadDomainOwned(c, domainID)
	if domain == nil {
		return
	}

	// Load SSL certificate (may not exist)
	cert, err := h.cfg.SSLCerts.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			// No certificate provisioned yet
			c.JSON(http.StatusNotFound, gin.H{"error": "no_certificate"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	resp := SSLResponse{
		Status:        cert.Status,
		IssuedAt:      cert.IssuedAt,
		ExpiresAt:     cert.ExpiresAt,
		RenewalCount:  cert.RenewalCount,
		LastRenewedAt: cert.LastRenewedAt,
		LastError:     cert.LastError,
		Staging:       cert.Staging,
		CertPath:      cert.CertPath,
		KeyPath:       cert.KeyPath,
	}

	c.JSON(http.StatusOK, gin.H{"ssl": resp})
}

// enableSSL enables SSL for a domain and initiates certificate issuance.
// POST /api/v1/domains/:id/ssl
// Response: 202 (Accepted) with the pending certificate row.
// Idempotent: if an issued cert exists with >30d to expiry, return 200 with it.
func (h *sslHandler) enableSSL(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	domain := h.loadDomainOwned(c, domainID)
	if domain == nil {
		return
	}

	ctx := c.Request.Context()

	// Check server_settings.admin_email is configured
	settings, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if settings.AdminEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "missing_admin_email",
			"detail": "server_settings.admin_email must be configured to enable SSL",
		})
		return
	}

	// Check if an issued cert already exists with >30d to expiry
	cert, err := h.cfg.SSLCerts.FindByDomainID(ctx, domainID)
	if err == nil && cert != nil && cert.Status == models.SSLStatusIssued && cert.ExpiresAt != nil {
		daysToExpiry := time.Until(*cert.ExpiresAt).Hours() / 24
		if daysToExpiry > 30 {
			// Idempotent: return 200 with the existing cert
			resp := SSLResponse{
				Status:        cert.Status,
				IssuedAt:      cert.IssuedAt,
				ExpiresAt:     cert.ExpiresAt,
				RenewalCount:  cert.RenewalCount,
				LastRenewedAt: cert.LastRenewedAt,
				LastError:     cert.LastError,
				Staging:       cert.Staging,
				CertPath:      cert.CertPath,
				KeyPath:       cert.KeyPath,
			}
			c.JSON(http.StatusOK, gin.H{"ssl": resp})
			return
		}
	}

	// Set TLS mode = le (GH #246; this legacy endpoint is "enable ACME").
	if err := h.cfg.Domains.UpdateSSLMode(ctx, domain.ID, models.SSLModeLE); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	domain.SSLMode = models.SSLModeLE
	domain.SSLEnabled = true

	// Create or update certificate row with status=pending
	if cert == nil {
		// Create new certificate row
		cert = &models.SSLCertificate{
			ID:       ids.NewULID(),
			DomainID: domainID,
			Status:   models.SSLStatusPending,
			Staging:  h.cfg.Config.ACME.StagingOnly,
		}
		if err := h.cfg.SSLCerts.Create(ctx, cert); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	} else {
		// Update existing to pending
		if err := h.cfg.SSLCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusPending, nil); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	}

	// Schedule reconciliation
	h.cfg.Reconciler.Schedule(domainID)

	resp := SSLResponse{
		Status:        cert.Status,
		IssuedAt:      cert.IssuedAt,
		ExpiresAt:     cert.ExpiresAt,
		RenewalCount:  cert.RenewalCount,
		LastRenewedAt: cert.LastRenewedAt,
		LastError:     cert.LastError,
		Staging:       cert.Staging,
		CertPath:      cert.CertPath,
		KeyPath:       cert.KeyPath,
	}

	c.JSON(http.StatusAccepted, gin.H{"ssl": resp})
}

// disableSSL disables SSL for a domain and revokes the certificate.
// DELETE /api/v1/domains/:id/ssl
// Response: 202 (Accepted).
func (h *sslHandler) disableSSL(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	domain := h.loadDomainOwned(c, domainID)
	if domain == nil {
		return
	}

	ctx := c.Request.Context()

	// Set TLS mode = none (GH #246; this legacy endpoint is "disable TLS").
	if err := h.cfg.Domains.UpdateSSLMode(ctx, domain.ID, models.SSLModeNone); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	domain.SSLMode = models.SSLModeNone
	domain.SSLEnabled = false

	// Mark certificate row as revoked (if it exists)
	cert, err := h.cfg.SSLCerts.FindByDomainID(ctx, domainID)
	if err == nil && cert != nil {
		if err := h.cfg.SSLCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusRevoked, nil); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	}

	// Schedule reconciliation
	h.cfg.Reconciler.Schedule(domainID)

	c.Status(http.StatusAccepted)
}

// renewSSL triggers an immediate renewal of an SSL certificate.
// POST /api/v1/domains/:id/ssl/renew
// Admin-only. Response: 202 (Accepted).
func (h *sslHandler) renewSSL(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain (admin-only)
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	if !claims.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	ctx := c.Request.Context()

	// Load certificate row
	cert, err := h.cfg.SSLCerts.FindByDomainID(ctx, domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no_certificate"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Mark as renewing
	if err := h.cfg.SSLCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusRenewing, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Schedule reconciliation
	h.cfg.Reconciler.Schedule(domainID)

	c.Status(http.StatusAccepted)
}

// retrySSL manually retries ACME issuance for a failed certificate.
// POST /api/v1/domains/:id/ssl/retry
func (h *sslHandler) retrySSL(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain (admin-only)
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	if !claims.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	ctx := c.Request.Context()

	// Load certificate row
	cert, err := h.cfg.SSLCerts.FindByDomainID(ctx, domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no_certificate"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Only allow retry on failed or pending_acme_retry statuses
	if cert.Status != models.SSLStatusFailed && cert.Status != models.SSLStatusPendingACMERetry {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot_retry"})
		return
	}

	// Reset retry count and clear next_retry_at to allow immediate retry
	if err := h.cfg.SSLCerts.UpdateStatus(ctx, cert.ID, cert.Status, cert.LastError); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// For pending_acme_retry, reset retry_count to 0
	// We'll do this via a direct update since we don't have a dedicated method
	// Actually, let's just mark it as pending to trigger immediate retry
	if err := h.cfg.SSLCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusPending, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Schedule reconciliation to attempt ACME
	h.cfg.Reconciler.Schedule(domainID)

	c.Status(http.StatusAccepted)
}

// listAllSSL lists all SSL certificates across all users (admin-only).
// GET /api/v1/admin/ssl-certificates
func (h *sslHandler) listAllSSL(c *gin.Context) {
	ctx := c.Request.Context()
	certs, err := h.cfg.SSLCerts.ListAll(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Panel-hostname + panel-mail certs live in panel_certificate
	// (separate table; ADR-0066/0105 + M32) and don't appear in the
	// ssl_certificates join above. Surface them here so the admin SSL
	// Manager shows every cert managed by the panel in one place.
	// Synthetic IDs are prefixed "panel-cert:" so the UI can disable
	// per-domain Renew (panel cert renews via its own scheduler).
	if h.cfg.PanelCerts != nil && h.cfg.Domains != nil {
		certs = append(panelCertSyntheticRows(ctx, h.cfg), certs...)
	}

	// Per-domain mail certs (mail_certificate table, M6.6) live separately
	// from ssl_certificates. Surface them here as "mail-cert:<domainID>" rows
	// so the SSL Manager shows + can reissue every cert the panel manages
	// (GH#132: tenants couldn't find/reissue the mail cert anywhere).
	if h.cfg.MailCerts != nil {
		if mailRows, err := h.cfg.MailCerts.ListWithDomain(ctx); err == nil {
			certs = append(certs, mailRows...)
		}
	}

	c.JSON(http.StatusOK, gin.H{"items": h.enrichCertRows(c.Request.Context(), certs)})
}

// panelCertSyntheticRows projects the panel_certificate rows
// (hostname + mail kinds) into SSLCertificateWithDomain entries so
// they render in the admin Domain Certificates table alongside
// regular tenant certs. Returns empty on any error or missing row —
// best-effort, never blocks the main listing.
func panelCertSyntheticRows(ctx context.Context, cfg SSLHandlerConfig) []repository.SSLCertificateWithDomain {
	primary, err := cfg.Domains.FindPanelPrimary(ctx)
	if err != nil || primary == nil {
		return nil
	}
	panelCerts, err := cfg.PanelCerts.ListAll(ctx)
	if err != nil {
		return nil
	}
	out := make([]repository.SSLCertificateWithDomain, 0, len(panelCerts))
	for _, pc := range panelCerts {
		if pc == nil {
			continue
		}
		domainName := primary.Name
		if pc.Kind == models.PanelCertKindMail {
			domainName = models.PanelMailHostname(primary.Name)
		}
		out = append(out, repository.SSLCertificateWithDomain{
			ID:           "panel-cert:" + pc.Kind,
			DomainID:     primary.ID,
			DomainName:   domainName,
			UserID:       primary.UserID,
			UserUsername: "system",
			Status:       pc.Status,
			IssuedAt:     pc.IssuedAt,
			ExpiresAt:    pc.ExpiresAt,
			Staging:      pc.Staging,
			LastError:    nilIfEmpty(pc.LastError),
		})
	}
	return out
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// listUserSSL lists all SSL certificates for the caller's domains.
// GET /api/v1/ssl-certificates
func (h *sslHandler) listUserSSL(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	certs, err := h.cfg.SSLCerts.ListByUserID(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Per-domain mail certs the caller owns, projected as "mail-cert:<id>"
	// rows so they appear (+ are reissuable) in the user SSL Manager (GH#132).
	if h.cfg.MailCerts != nil {
		if mailRows, err := h.cfg.MailCerts.ListWithDomainByUser(c.Request.Context(), claims.UserID); err == nil {
			certs = append(certs, mailRows...)
		}
	}

	c.JSON(http.StatusOK, gin.H{"items": h.enrichCertRows(c.Request.Context(), certs)})
}

// loadDomainOwned fetches the domain by ID and enforces that the
// caller is either the owner or an admin. Returns nil if successful,
// otherwise responds to c and returns nil.
func (h *sslHandler) loadDomainOwned(c *gin.Context, domainID string) *models.Domain {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil
	}

	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil
	}

	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil
	}

	return domain
}

// sslListRow is a cert row plus the two fields GH #195 adds to the SSL
// Manager: the service the cert secures and the full alias (SAN) list,
// so aliases render under the primary record.
type sslListRow struct {
	repository.SSLCertificateWithDomain
	Service string   `json:"service"`
	SANs    []string `json:"sans"`
}

// enrichCertRows attaches Service + SANs to every cert row. SANs are
// derived from the same policy issuance uses (mirrors the reconciler's
// sanHostnamesForDomain + the agent's mailSANHostnames), so the panel
// shows what the issued cert covers without reading cert files.
func (h *sslHandler) enrichCertRows(ctx context.Context, certs []repository.SSLCertificateWithDomain) []sslListRow {
	out := make([]sslListRow, 0, len(certs))
	for _, c := range certs {
		row := sslListRow{SSLCertificateWithDomain: c}
		switch {
		case c.ID == "panel-cert:"+models.PanelCertKindMail:
			row.Service = "Panel mail"
			row.SANs = []string{c.DomainName}
		case strings.HasPrefix(c.ID, "panel-cert:"):
			row.Service = "Panel (HTTPS)"
			row.SANs = []string{c.DomainName}
		case strings.HasPrefix(c.ID, "mail-cert:"):
			row.Service = "Mail (SMTPS, IMAPS)"
			row.SANs = h.mailCertSANs(ctx, c)
		default:
			row.Service = "HTTPS"
			row.SANs = h.webCertSANs(ctx, c)
		}
		out = append(out, row)
	}
	return out
}

// webCertSANs returns the SAN list of a website (HTTPS) cert: base
// [domain, www.domain] plus the auto-added names. Falls back to the
// base pair if the domain can't be loaded.
func (h *sslHandler) webCertSANs(ctx context.Context, c repository.SSLCertificateWithDomain) []string {
	base := []string{c.DomainName, "www." + c.DomainName}
	if h.cfg.Domains == nil {
		return base
	}
	d, err := h.cfg.Domains.FindByID(ctx, c.DomainID)
	if err != nil || d == nil {
		return base
	}
	return webCertSANsForDomain(d)
}

// webCertSANsForDomain is the pure policy (extracted for testing) — it
// MUST stay in lockstep with reconciler.sanHostnamesForDomain (ADR-0070).
func webCertSANsForDomain(d *models.Domain) []string {
	sans := []string{d.Name, "www." + d.Name}
	if d.SkipAutoSAN {
		return sans
	}
	if d.EmailEnabled {
		sans = append(sans, "mail."+d.Name, "autoconfig."+d.Name, "autodiscover."+d.Name)
	}
	if d.MTASTSEnabled {
		sans = append(sans, "mta-sts."+d.Name)
	}
	return sans
}

// mailCertSANs returns the SAN list of a per-domain Stalwart mail cert.
// The row's DomainName is the cert's primary hostname (mail.<base>); we
// strip that label to get the base domain. mta-sts.<base> is included
// ONLY when MTA-STS is enabled — its DNS record exists only then, so the
// agent's issuance drops it otherwise (selectEffectiveSANs), and showing
// it would over-report (GH #195, johnnyq). Falls back to no-mta-sts when
// the domain can't be loaded.
func (h *sslHandler) mailCertSANs(ctx context.Context, c repository.SSLCertificateWithDomain) []string {
	base := strings.TrimPrefix(c.DomainName, "mail.")
	mtasts := false
	if h.cfg.Domains != nil {
		if d, err := h.cfg.Domains.FindByID(ctx, c.DomainID); err == nil && d != nil {
			mtasts = d.MTASTSEnabled
		}
	}
	return mailCertSANsForBase(base, mtasts)
}

// mailCertSANsForBase is the pure SAN policy (extracted for testing) —
// mail/autoconfig/autodiscover always (their CNAMEs ship with email),
// plus mta-sts only when enabled.
func mailCertSANsForBase(base string, mtastsEnabled bool) []string {
	base = strings.TrimSpace(strings.ToLower(base))
	sans := []string{"mail." + base, "autoconfig." + base, "autodiscover." + base}
	if mtastsEnabled {
		sans = append(sans, "mta-sts."+base)
	}
	return sans
}

// customCertRequest is the body for PUT /domains/:id/ssl/custom (GH #246).
type customCertRequest struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// installCustomSSL installs an operator-supplied cert+key for a domain and
// switches it to ssl_mode=custom (GH #246). The agent validates the cert/key
// pair and writes them 0600; panel-api parses not-after for the cert row.
// The private key is never logged or echoed back.
func (h *sslHandler) installCustomSSL(c *gin.Context) {
	domainID := c.Param("id")
	domain := h.loadDomainOwned(c, domainID)
	if domain == nil {
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	var req customCertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	if strings.TrimSpace(req.CertPEM) == "" || strings.TrimSpace(req.KeyPEM) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cert_and_key_required"})
		return
	}

	// Parse not-after + check the cert covers the domain (warn-only via 400
	// when it clearly doesn't, so an operator can't pin a wrong cert).
	leaf, notAfter, err := parseLeafNotAfter(req.CertPEM)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cert", "detail": err.Error()})
		return
	}
	if leaf.VerifyHostname(domain.Name) != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "cert_domain_mismatch",
			"detail": fmt.Sprintf("certificate does not cover %s", domain.Name),
		})
		return
	}

	ctx := c.Request.Context()
	raw, err := h.cfg.Agent.Call(ctx, "ssl.install_custom", map[string]any{
		"domain":   domain.Name,
		"cert_pem": req.CertPEM,
		"key_pem":  req.KeyPEM,
	})
	if err != nil {
		// Agent validation errors (bad pair, parse) are the caller's fault.
		c.JSON(http.StatusBadRequest, gin.H{"error": "install_failed", "detail": firstLineSSL(err.Error())})
		return
	}
	var res struct {
		CertPath string `json:"cert_path"`
		KeyPath  string `json:"key_path"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.CertPath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Authoritative: switch mode to custom (sets ssl_enabled shadow too).
	if err := h.cfg.Domains.UpdateSSLMode(ctx, domain.ID, models.SSLModeCustom); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Upsert the cert row (status=custom, paths, parsed expiry).
	cert, _ := h.cfg.SSLCerts.FindByDomainID(ctx, domain.ID)
	if cert == nil {
		cert = &models.SSLCertificate{
			ID:        ids.NewULID(),
			DomainID:  domain.ID,
			Status:    models.SSLStatusCustom,
			CertPath:  &res.CertPath,
			KeyPath:   &res.KeyPath,
			ExpiresAt: &notAfter,
		}
		if err := h.cfg.SSLCerts.Create(ctx, cert); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	} else if err := h.cfg.SSLCerts.UpdateCustom(ctx, cert.ID, res.CertPath, res.KeyPath, notAfter); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}
	c.JSON(http.StatusOK, gin.H{"ssl": gin.H{
		"status":     models.SSLStatusCustom,
		"cert_path":  res.CertPath,
		"key_path":   res.KeyPath,
		"expires_at": notAfter,
	}})
}

// parseLeafNotAfter extracts the leaf certificate + its NotAfter from a PEM
// chain. Never logs the input.
func parseLeafNotAfter(certPEM string) (*x509.Certificate, time.Time, error) {
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, time.Time{}, fmt.Errorf("no CERTIFICATE PEM block found")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("parse certificate: %w", err)
		}
		return leaf, leaf.NotAfter, nil
	}
}

func firstLineSSL(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
