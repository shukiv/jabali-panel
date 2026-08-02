// Per-domain mail TLS (M6.6).
//
// Routes (mounted under /api/v1/domains/:id/mail-certificate):
//
//	GET     status + last_error + expires_at
//	POST    /enable  → create or unblock the row (status=pending)
//	POST    /disable → opt-out (status=disabled)
//	POST    /reissue → clear backoff + flip back to pending so the
//	                   next reconciler tick re-runs ssl.mail.issue
//
// Authorization: admins read+write any; users only their own domains.
// Cross-tenant access returns 404 to avoid leaking domain existence.
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type DomainMailCertHandlerConfig struct {
	Domains   repository.DomainRepository
	MailCerts repository.MailCertificateRepository
	// Reconciler hook so a mutation kicks an immediate converge
	// instead of waiting for the next tick.
	Reconcile func(domainID string)
}

func RegisterDomainMailCertificateRoutes(g *gin.RouterGroup, cfg DomainMailCertHandlerConfig) {
	if cfg.Domains == nil || cfg.MailCerts == nil {
		return
	}
	h := &domainMailCertHandler{cfg: cfg}
	rg := g.Group("/domains/:id/mail-certificate")
	rg.GET("", h.get)
	rg.POST("/enable", h.enable)
	rg.POST("/disable", h.disable)
	rg.POST("/reissue", h.reissue)
}

type domainMailCertHandler struct{ cfg DomainMailCertHandlerConfig }

// resolveDomain mirrors the IP ACL handler's logic. Centralisation
// would be nice but the existing helper is on a different handler
// struct; copy keeps the call sites typed.
func (h *domainMailCertHandler) resolveDomain(c *gin.Context) (*models.Domain, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil, false
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil, false
	}
	return dom, true
}

type mailCertResponse struct {
	Status       string  `json:"status"`
	LineagePath  string  `json:"lineage_path,omitempty"`
	LastError    *string `json:"last_error,omitempty"`
	AttemptCount uint32  `json:"attempt_count"`
	IssuedAt     *string `json:"issued_at,omitempty"`
	ExpiresAt    *string `json:"expires_at,omitempty"`
	NextRetryAt  *string `json:"next_retry_at,omitempty"`
	// SANs is the constant 4-hostname set the UI renders in the DNS
	// table. Returned even when no row exists so the UI can render the
	// opt-in CTA next to a preview of which records the user will
	// need to point at the panel.
	SANs []string `json:"sans"`
}

func (h *domainMailCertHandler) get(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	sans := mailSANHostnames(dom.Name)
	row, err := h.cfg.MailCerts.GetByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusOK, mailCertResponse{
				Status: "not_opted_in",
				SANs:   sans,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, mailCertResponseFromRow(row, sans))
}

func (h *domainMailCertHandler) enable(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	row, err := h.cfg.MailCerts.EnsureForDomain(c.Request.Context(), dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed", "detail": err.Error()})
		return
	}
	// If the row already existed but was disabled, flip it back to
	// pending so the next reconciler tick picks it up.
	if row.Status == models.MailCertStatusDisabled {
		if uerr := h.cfg.MailCerts.UpdateStatus(c.Request.Context(), row.ID, models.MailCertStatusPending, nil); uerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
			return
		}
		row.Status = models.MailCertStatusPending
	}
	h.kick(dom.ID)
	c.JSON(http.StatusOK, mailCertResponseFromRow(row, mailSANHostnames(dom.Name)))
}

func (h *domainMailCertHandler) disable(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	row, err := h.cfg.MailCerts.GetByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.Status(http.StatusNoContent)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if uerr := h.cfg.MailCerts.UpdateStatus(c.Request.Context(), row.ID, models.MailCertStatusDisabled, nil); uerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *domainMailCertHandler) reissue(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	row, err := h.cfg.MailCerts.GetByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "not_opted_in"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Clear backoff + flip to pending. The next reconciler tick picks
	// it up; the operator doesn't need to wait for the 3h failed
	// window to elapse.
	if uerr := h.cfg.MailCerts.UpdateStatus(c.Request.Context(), row.ID, models.MailCertStatusPending, nil); uerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}
	row.Status = models.MailCertStatusPending
	h.kick(dom.ID)
	c.JSON(http.StatusOK, mailCertResponseFromRow(row, mailSANHostnames(dom.Name)))
}

func (h *domainMailCertHandler) kick(domainID string) {
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(domainID)
	}
}

// mailCertResponseFromRow projects the DB row into the response
// envelope, formatting timestamps as RFC3339 strings (or nil).
func mailCertResponseFromRow(row *models.MailCertificate, sans []string) mailCertResponse {
	out := mailCertResponse{
		Status:       row.Status,
		LineagePath:  row.LineagePath,
		LastError:    row.LastError,
		AttemptCount: row.AttemptCount,
		SANs:         sans,
	}
	if row.IssuedAt != nil {
		s := row.IssuedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.IssuedAt = &s
	}
	if row.ExpiresAt != nil {
		s := row.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		out.ExpiresAt = &s
	}
	if row.NextRetryAt != nil {
		s := row.NextRetryAt.UTC().Format("2006-01-02T15:04:05Z")
		out.NextRetryAt = &s
	}
	return out
}

// mailSANHostnames duplicates panel-agent's hostname list so the API
// + UI can render it without an agent round-trip. Keep these two in
// sync by hand; the test (TODO step 8) asserts the order.
func mailSANHostnames(domain string) []string {
	domain = sanitiseDomainHost(domain)
	return []string{
		"mail." + domain,
		"autoconfig." + domain,
		"autodiscover." + domain,
		"mta-sts." + domain,
	}
}

func sanitiseDomainHost(d string) string {
	// Lowercase + trim. Domain rows are already validated at create
	// time; no need for full regex check here.
	out := make([]byte, 0, len(d))
	for i := 0; i < len(d); i++ {
		ch := d[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			out = append(out, ch+32)
		case ch == ' ' || ch == '\t':
			// drop
		default:
			out = append(out, ch)
		}
	}
	return string(out)
}
