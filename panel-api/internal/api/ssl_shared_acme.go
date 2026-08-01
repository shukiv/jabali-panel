package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ssl_shared_acme.go — request a panel-ISSUED shared certificate via ACME
// DNS-01 (wildcards), as opposed to the upload flow in ssl_shared.go.
//
// The handler only validates and inserts a pending row; the actual
// certbot run happens on the reconciler's next tick (LE DNS-01 takes
// tens of seconds — far too long to hold an HTTP request open against
// the agent socket). The UI watches the shared-cert list for the row to
// gain cert paths (issued) or a last_error.

type sharedCertAcmeRequest struct {
	// DomainID names the domain the wildcard should cover; the resulting
	// SAN set is [name, *.name]. The domain's DNS zone must be hosted on
	// this panel — DNS-01 places TXT records through the local pdns.
	DomainID string `json:"domain_id"`
	// Staging issues against the LE staging CA (untrusted; testing only).
	Staging bool `json:"staging"`
}

func (h *sslHandler) requestAcmeSharedCert(c *gin.Context) {
	if h.cfg.SharedCerts == nil || h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	if h.cfg.DNSZones == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dns_unavailable", "detail": "DNS module is not enabled — DNS-01 needs the domain's zone hosted here"})
		return
	}
	var req sharedCertAcmeRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.DomainID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain_id_required"})
		return
	}
	ctx := c.Request.Context()
	domain, err := h.cfg.Domains.FindByID(ctx, req.DomainID)
	if err != nil || domain == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}
	zone, err := h.cfg.DNSZones.FindByDomainID(ctx, domain.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "dns_zone_not_local", "detail": "the domain's DNS zone is not hosted on this panel — DNS-01 cannot place the challenge record"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !zone.IsEnabled {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "dns_zone_disabled"})
		return
	}
	if h.cfg.ServerSettings != nil {
		if srv, serr := h.cfg.ServerSettings.Get(ctx); serr != nil || srv == nil || strings.TrimSpace(srv.AdminEmail) == "" {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "admin_email_required", "detail": "set the admin email in server settings — Let's Encrypt requires a contact address"})
			return
		}
	}

	sans := []string{domain.Name, "*." + domain.Name}
	sansBytes, _ := json.Marshal(sans)
	sansJSON := string(sansBytes)
	now := time.Now().UTC()
	owner := domain.UserID
	cert := &models.SharedCertificate{
		ID:        ids.NewULID(),
		Name:      "*." + domain.Name,
		UserID:    &owner,
		SANs:      &sansJSON,
		Source:    models.SharedCertSourceACME,
		Staging:   req.Staging,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.cfg.SharedCerts.Create(ctx, cert); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"id":     cert.ID,
		"name":   cert.Name,
		"sans":   sans,
		"source": cert.Source,
		"status": "pending",
		"detail": "issuance runs on the next reconcile tick; watch the shared-certificates list",
	})
}
