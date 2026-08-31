package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
)

// backup_restore_upload.go — GH #1408. Admin "restore from an uploaded backup
// archive" (DR / cross-server migration): upload the .tar an admin downloaded
// earlier, inspect it, then restore selected components into an existing user.
//
// Upload is CHUNKED (reuses the #1323 pattern that already beat Cloudflare's
// origin timeout on big DB restores): each chunk is an octet-stream POST
// appended at `offset`; the file reassembles under /var/lib/jabali-uploads (the
// agent-readable handoff dir). The path is derived from the AUTHENTICATED admin
// + the client upload_id, so one admin can never point the restore at another's
// staged file. The apply is step-up gated (destructive) and hands the reassembled
// tar to the agent's untrusted-tar extractor (backup.restore_from_tar).

const (
	restoreUploadDir      = "/var/lib/jabali-uploads"
	restoreUploadTTL      = 24 * time.Hour
	maxInFlightRestoreTar = 2
	// maxRestoreUploadBytes bounds a single reassembled archive so a stuck /
	// malicious upload can't fill the service partition before the extractor's
	// own budget kicks in. 300 GiB covers the largest realistic per-account tar.
	maxRestoreUploadBytes = int64(300) << 30
)

var (
	uploadIDRE               = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	restoreTargetUsernameRE  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

func restoreUploadAdminTag(userID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-upload-admin:" + userID))
	return hex.EncodeToString(sum[:])[:16]
}

// restoreUploadPath derives the reassembly path from the server-side admin
// userID + the client upload_id (Gitea #426 pattern), so it can't be steered
// across admins or out of the uploads dir.
func restoreUploadPath(adminUserID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-upload:" + adminUserID + ":" + uploadID))
	return filepath.Join(restoreUploadDir, "rup-"+restoreUploadAdminTag(adminUserID)+"-"+hex.EncodeToString(sum[:])+".tar.zst")
}

func evictStaleRestoreUploads(adminUserID string) {
	matches, _ := filepath.Glob(filepath.Join(restoreUploadDir, "rup-"+restoreUploadAdminTag(adminUserID)+"-*.tar.zst"))
	cutoff := time.Now().Add(-restoreUploadTTL)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(m)
		}
	}
}

func (h *backupHandler) restoreUploadAdminID(c *gin.Context) (string, bool) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	return claims.UserID, true
}

// restoreUploadChunk handles POST /admin/backups/restore-upload?upload_id=&offset=[&final=1].
// Body is a raw octet-stream chunk appended at offset. Exempt from the global
// body limit (see bodyLimitExemptRoutes).
func (h *backupHandler) restoreUploadChunk(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	if !uploadIDRE.MatchString(uploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	offset, err := strconv.ParseInt(c.Query("offset"), 10, 64)
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_offset"})
		return
	}
	if err := os.MkdirAll(restoreUploadDir, 0o750); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "staging_unavailable"})
		return
	}
	evictStaleRestoreUploads(adminID)

	path := restoreUploadPath(adminID, uploadID)
	if offset == 0 {
		if _, statErr := os.Stat(path); statErr != nil {
			matches, _ := filepath.Glob(filepath.Join(restoreUploadDir, "rup-"+restoreUploadAdminTag(adminID)+"-*.tar.zst"))
			if len(matches) >= maxInFlightRestoreTar {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_uploads", "detail": "finish or wait for an in-flight restore upload"})
				return
			}
		}
	}
	if offset >= maxRestoreUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "archive_too_large"})
		return
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open_staging_failed"})
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seek_failed"})
		return
	}
	// LimitReader bounds the write so a chunk can't push the file past the cap;
	// the extractor enforces the real decompression-bomb budget later.
	written, werr := io.Copy(f, io.LimitReader(c.Request.Body, maxRestoreUploadBytes-offset))
	if werr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "write_failed", "detail": werr.Error()})
		return
	}
	if c.Query("final") == "1" {
		size := offset + written
		if fi, serr := f.Stat(); serr == nil {
			size = fi.Size()
		}
		c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "size": size})
		return
	}
	c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "written": written, "offset": offset + written})
}

type restoreUploadInspectRequest struct {
	UploadID string `json:"upload_id"`
}

// restoreUploadInspect returns the backup's user + restorable components for the
// UI to present a selection before the destructive apply.
func (h *backupHandler) restoreUploadInspect(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	var req restoreUploadInspectRequest
	if err := c.ShouldBindJSON(&req); err != nil || !uploadIDRE.MatchString(req.UploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	path := restoreUploadPath(adminID, req.UploadID)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "backup.inspect_uploaded_tar", map[string]string{"tar_path": path})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

type restoreUploadApplyRequest struct {
	UploadID       string   `json:"upload_id"`
	TargetUsername string   `json:"target_username"`
	Components     []string `json:"components,omitempty"`
}

// restoreUploadApply restores the uploaded archive into an EXISTING user.
// Destructive → step-up gated. Reuses applyRestoreMetadata (JAB-312) to rebuild
// the panel DB rows from the backup's metadata bundle, exactly like the snapshot
// restore path.
func (h *backupHandler) restoreUploadApply(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	// Destructive admin action → recent-auth (MFA step-up), same bar as the
	// admin File Manager's sensitive operations.
	if !requireRecentAuth(c, h.cfg.KratosClient, stepUpWindow) {
		return
	}
	var req restoreUploadApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil || !uploadIDRE.MatchString(req.UploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if !restoreTargetUsernameRE.MatchString(req.TargetUsername) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target_username"})
		return
	}
	// v1: the target user must already exist (no create-from-manifest).
	target, uerr := h.cfg.Users.FindByUsername(c.Request.Context(), req.TargetUsername)
	if uerr != nil || target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "target_user_not_found", "detail": "create the user first, then restore into it"})
		return
	}
	path := restoreUploadPath(adminID, req.UploadID)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}

	c.Set("audit_target", req.TargetUsername)
	c.Set("audit_target_type", "user")

	raw, err := h.cfg.Agent.Call(c.Request.Context(), "backup.restore_from_tar", map[string]any{
		"job_id":          ids.NewULID(),
		"tar_path":        path,
		"target_username": req.TargetUsername,
		"components":      req.Components,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result struct {
		User     json.RawMessage `json:"user"`
		Applied  []string        `json:"applied"`
		Warnings []string        `json:"warnings"`
		Metadata json.RawMessage `json:"metadata"`
	}
	_ = json.Unmarshal(raw, &result)

	// Rebuild panel DB rows (domains, mailboxes, db-users, …) from the backup's
	// metadata bundle — the same step the snapshot restore runs (JAB-312).
	var metaErrs []string
	if len(result.Metadata) > 0 {
		metaErrs = h.applyRestoreMetadata(c.Request.Context(), result.Metadata)
	}

	// The archive is consumed — drop it so a downloaded backup with real data
	// doesn't linger in the uploads dir.
	_ = os.Remove(path)

	c.JSON(http.StatusOK, gin.H{
		"status":          "ok",
		"user":            result.User,
		"applied":         result.Applied,
		"warnings":        result.Warnings,
		"metadata_errors": metaErrs,
	})
}
