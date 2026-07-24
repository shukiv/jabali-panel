package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
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

// --- attach / detach / replace (JAB-170 phase 4b) ---

type attachSharedRequest struct {
	SharedCertificateID string `json:"shared_certificate_id"`
}

// hostMatchesSAN reports whether a cert SAN covers host — x509.VerifyHostname
// semantics: exact match, or a single-label wildcard (*.example.com matches
// sub.example.com but NOT example.com or a.b.example.com).
func hostMatchesSAN(san, host string) bool {
	san = strings.ToLower(strings.TrimSpace(san))
	host = strings.ToLower(strings.TrimSpace(host))
	if san == "" || host == "" {
		return false
	}
	if san == host {
		return true
	}
	if strings.HasPrefix(san, "*.") {
		base := san[2:]
		if i := strings.IndexByte(host, '.'); i > 0 && host[i+1:] == base {
			return true
		}
	}
	return false
}

func sharedCertCoversHost(sansJSON *string, host string) bool {
	if sansJSON == nil {
		return false
	}
	var sans []string
	if json.Unmarshal([]byte(*sansJSON), &sans) != nil {
		return false
	}
	for _, sn := range sans {
		if hostMatchesSAN(sn, host) {
			return true
		}
	}
	return false
}

// attachSharedCert points a domain at a shared cert (ssl_mode=shared). Ownership:
// the caller must own the domain (loadDomainOwned) AND the cert must be
// server-wide or theirs; the cert's SANs must cover the domain.
func (h *sslHandler) attachSharedCert(c *gin.Context) {
	if h.cfg.SharedCerts == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	domain := h.loadDomainOwned(c, c.Param("id"))
	if domain == nil {
		return
	}
	var req attachSharedRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.SharedCertificateID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "shared_certificate_id_required"})
		return
	}
	ctx := c.Request.Context()
	cert, err := h.cfg.SharedCerts.FindByID(ctx, req.SharedCertificateID)
	if err != nil || cert == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate_not_found"})
		return
	}
	claims := ginctx.Claims(c)
	if cert.UserID != nil && (claims == nil || (!claims.IsAdmin && *cert.UserID != claims.UserID)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "detail": "not your certificate"})
		return
	}
	if !sharedCertCoversHost(cert.SANs, domain.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cert_domain_mismatch", "detail": fmt.Sprintf("certificate does not cover %s", domain.Name)})
		return
	}
	id := cert.ID
	if err := h.cfg.Domains.SetSharedCertificate(ctx, domain.ID, &id, models.SSLModeShared); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}
	c.JSON(http.StatusOK, gin.H{"attached": true, "shared_certificate_id": id})
}

// detachSharedCert reverts a domain off its shared cert back to LE mode.
func (h *sslHandler) detachSharedCert(c *gin.Context) {
	domain := h.loadDomainOwned(c, c.Param("id"))
	if domain == nil {
		return
	}
	if err := h.cfg.Domains.SetSharedCertificate(c.Request.Context(), domain.ID, nil, models.SSLModeLE); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}
	c.JSON(http.StatusOK, gin.H{"detached": true})
}

// replaceSharedCert re-uploads the cert/key for an existing shared cert. The
// agent overwrites the pair at the same path and reloads nginx, so every
// attached domain serves the new cert in one action. Admin.
func (h *sslHandler) replaceSharedCert(c *gin.Context) {
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
	var req sharedCertUploadRequest // reuse shape; name ignored on replace
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	if strings.TrimSpace(req.CertPEM) == "" || strings.TrimSpace(req.KeyPEM) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cert_and_key_required"})
		return
	}
	if _, _, perr := parseLeafNotAfter(req.CertPEM); perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cert", "detail": perr.Error()})
		return
	}
	raw, err := h.cfg.Agent.Call(ctx, "ssl.install_shared", map[string]any{"id": id, "cert_pem": req.CertPEM, "key_pem": req.KeyPEM})
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
	expiresAt, _ := time.Parse(time.RFC3339, res.ExpiresAt)
	sansJSON, _ := json.Marshal(res.SANs)
	if err := h.cfg.SharedCerts.UpdateInstalled(ctx, id, res.CertPath, res.KeyPath, string(sansJSON), expiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "sans": res.SANs, "expires_at": res.ExpiresAt})
}
