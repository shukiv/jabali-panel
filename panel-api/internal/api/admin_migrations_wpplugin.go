// admin_migrations_wpplugin.go — GH #648 S2. Admin key-management for the
// WordPress plugin push migration: mint a one-time key (shown ONCE), revoke it,
// read status. Owner-scoping for tenant-facing creation is a follow-on (S11) —
// this admin path satisfies the "admin can generate a key" acceptance criterion.
// The public claim/upload endpoints (S3+) live separately and never require an
// authenticated session.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

const migrationKeyTTL = 24 * time.Hour

// sha256Hex is the single hashing helper for keys + session tokens. Only these
// hashes are ever persisted.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// newMigrationSecret returns a 256-bit random hex token (the plaintext key or
// upload-session token). Caller stores only sha256Hex(token).
func newMigrationSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// registerWPPluginKeyRoutes adds the admin key-management endpoints. Called from
// RegisterAdminMigrationRoutes only when cfg.Keys is wired.
func (h *adminMigrationsHandler) registerWPPluginKeyRoutes(rg *gin.RouterGroup) {
	rg.POST("/wordpress-plugin", h.createWPPluginKey)
	rg.POST("/:id/key/revoke", h.revokeWPPluginKey)
	rg.GET("/:id/key", h.getWPPluginKey)
}

type createWPPluginKeyRequest struct {
	DestUserID   string `json:"dest_user_id"`
	DestDomainID string `json:"dest_domain_id"`
	DomainChange bool   `json:"domain_change"`
}

func (h *adminMigrationsHandler) createWPPluginKey(c *gin.Context) {
	if h.cfg.Keys == nil || h.cfg.Domains == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wp_plugin_migration_unavailable"})
		return
	}
	var req createWPPluginKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if req.DestUserID == "" || req.DestDomainID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dest_user_id and dest_domain_id are required"})
		return
	}
	ctx := c.Request.Context()

	// Resolve the destination domain → docroot, and verify the domain belongs
	// to the declared dest user (no cross-owner key even for admins — the key
	// must be bound to a coherent (user, domain) pair the import will trust).
	dom, err := h.cfg.Domains.FindByID(ctx, req.DestDomainID)
	if err != nil || dom == nil || dom.DocRoot == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "dest_domain_not_found"})
		return
	}
	if dom.UserID != req.DestUserID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain_not_owned_by_dest_user"})
		return
	}

	now := time.Now().UTC()
	jobID := ids.NewULID()
	// A wordpress_plugin job has no real source host/user until the plugin
	// claims; use the job ID as a unique placeholder so uq_migration_source
	// (host, user, kind) never collides across jobs. UpdateSourceUser pins the
	// real source URL on claim (S3).
	job := &models.MigrationJob{
		ID:           jobID,
		SourceKind:   models.MigrationSourceWordPressPlugin,
		SourceHost:   "wp-plugin",
		SourceUser:   jobID,
		TargetUserID: &req.DestUserID,
		State:        models.MigrationStatePending,
		StartedAt:    now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.cfg.Jobs.Create(ctx, job); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_job_failed"})
		return
	}

	plaintext, err := newMigrationSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "key_gen_failed"})
		return
	}
	key := &models.MigrationKey{
		ID:           ids.NewULID(),
		JobID:        jobID,
		KeyHash:      sha256Hex(plaintext),
		DestUserID:   req.DestUserID,
		DestDomainID: req.DestDomainID,
		DestDocroot:  dom.DocRoot,
		DomainChange: req.DomainChange,
		Status:       models.MigrationKeyUnclaimed,
		ExpiresAt:    now.Add(migrationKeyTTL),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.cfg.Keys.Create(ctx, key); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_key_failed"})
		return
	}

	// Plaintext key returned ONCE, never persisted or shown again.
	c.JSON(http.StatusCreated, gin.H{
		"job_id":     jobID,
		"key":        plaintext,
		"claim_url":  h.publicMigrationsBaseURL(ctx) + "/claim",
		"expires_at": key.ExpiresAt,
		"instructions": "Install the jabali-migrator plugin on the source WordPress site, " +
			"open its Migrate screen, and paste this key. The key is shown only once.",
	})
}

// publicMigrationsBaseURL builds the ingress base (A1): the panel hostname's
// public vhost, /public/migrations. Falls back to a relative path if the
// hostname is unset (dev).
func (h *adminMigrationsHandler) publicMigrationsBaseURL(ctx context.Context) string {
	host := ""
	if h.cfg.Settings != nil {
		if s, err := h.cfg.Settings.Get(ctx); err == nil && s != nil {
			host = s.Hostname
		}
	}
	if host == "" {
		return "/public/migrations"
	}
	return "https://" + host + "/public/migrations"
}

func (h *adminMigrationsHandler) revokeWPPluginKey(c *gin.Context) {
	if h.cfg.Keys == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wp_plugin_migration_unavailable"})
		return
	}
	ctx := c.Request.Context()
	key, err := h.cfg.Keys.FindByJobID(ctx, c.Param("id"))
	if err != nil || key == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key_not_found"})
		return
	}
	if err := h.cfg.Keys.Revoke(ctx, key.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revoke_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": models.MigrationKeyRevoked})
}

func (h *adminMigrationsHandler) getWPPluginKey(c *gin.Context) {
	if h.cfg.Keys == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wp_plugin_migration_unavailable"})
		return
	}
	key, err := h.cfg.Keys.FindByJobID(c.Request.Context(), c.Param("id"))
	if err != nil || key == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key_not_found"})
		return
	}
	// Never returns the key/hash — status + binding only (json:"-" on secrets).
	c.JSON(http.StatusOK, key)
}
