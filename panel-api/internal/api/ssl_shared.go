package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ssl_shared.go — admin API for JAB-170 shared wildcard/multi-SAN certificates:
// upload once, serve from many domains. Attach/detach (per-domain) lands in a
// later phase; this file covers upload / list / delete of server-wide certs.

type sharedCertUploadRequest struct {
	Name    string `json:"name"`
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

type sharedCertView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	SANs            []string `json:"sans"`
	ExpiresAt       string   `json:"expires_at,omitempty"`
	AttachedDomains int64    `json:"attached_domains"`
}

// uploadSharedCert validates + installs a server-wide shared cert. The agent
// writes the pair 0600 root-owned; the private key is never logged or echoed.
// Admin-only.
func (h *sslHandler) uploadSharedCert(c *gin.Context) {
	if h.cfg.Agent == nil || h.cfg.SharedCerts == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	var req sharedCertUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_required"})
		return
	}
	if strings.TrimSpace(req.CertPEM) == "" || strings.TrimSpace(req.KeyPEM) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cert_and_key_required"})
		return
	}
	// Pre-validate the cert parses (nicer 400 than a round-trip to the agent).
	if _, _, err := parseLeafNotAfter(req.CertPEM); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cert", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	id := ids.NewULID()
	raw, err := h.cfg.Agent.Call(ctx, "ssl.install_shared", map[string]any{
		"id":       id,
		"cert_pem": req.CertPEM,
		"key_pem":  req.KeyPEM,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "install_failed", "detail": firstLineSSL(err.Error())})
		return
	}
	var res struct {
		CertPath  string   `json:"cert_path"`
		KeyPath   string   `json:"key_path"`
		SANs      []string `json:"sans"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.CertPath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	var expiresAt *time.Time
	if t, perr := time.Parse(time.RFC3339, res.ExpiresAt); perr == nil {
		expiresAt = &t
	}
	sansJSON, _ := json.Marshal(res.SANs)
	sansStr := string(sansJSON)
	now := time.Now().UTC()
	cert := &models.SharedCertificate{
		ID:        id,
		Name:      req.Name,
		UserID:    nil, // server-wide (admin-owned); tenant-owned certs land later
		CertPath:  &res.CertPath,
		KeyPath:   &res.KeyPath,
		SANs:      &sansStr,
		ExpiresAt: expiresAt,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.cfg.SharedCerts.Create(ctx, cert); err != nil {
		// roll back the on-disk pair so a failed insert doesn't orphan a cert dir.
		_, _ = h.cfg.Agent.Call(ctx, "ssl.delete_shared", map[string]any{"id": id})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusCreated, sharedCertView{ID: id, Name: req.Name, SANs: res.SANs, ExpiresAt: res.ExpiresAt})
}

// listSharedCerts returns every shared cert + its attached-domain count. Admin.
func (h *sslHandler) listSharedCerts(c *gin.Context) {
	if h.cfg.SharedCerts == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	ctx := c.Request.Context()
	certs, err := h.cfg.SharedCerts.ListAll(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]sharedCertView, 0, len(certs))
	for i := range certs {
		v := sharedCertView{ID: certs[i].ID, Name: certs[i].Name}
		if certs[i].SANs != nil {
			_ = json.Unmarshal([]byte(*certs[i].SANs), &v.SANs)
		}
		if certs[i].ExpiresAt != nil {
			v.ExpiresAt = certs[i].ExpiresAt.UTC().Format(time.RFC3339)
		}
		v.AttachedDomains, _ = h.cfg.SharedCerts.CountAttachedDomains(ctx, certs[i].ID)
		out = append(out, v)
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// deleteSharedCert removes a shared cert (row + on-disk pair). Refuses while
// domains are still attached — they'd lose their cert. Admin.
func (h *sslHandler) deleteSharedCert(c *gin.Context) {
	if h.cfg.SharedCerts == nil || h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")
	cert, err := h.cfg.SharedCerts.FindByID(ctx, id)
	if err != nil || cert == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if n, _ := h.cfg.SharedCerts.CountAttachedDomains(ctx, id); n > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "has_attached_domains", "detail": "detach all domains before deleting"})
		return
	}
	if err := h.cfg.SharedCerts.Delete(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	_, _ = h.cfg.Agent.Call(ctx, "ssl.delete_shared", map[string]any{"id": id})
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}
