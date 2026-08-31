package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	tag := restoreUploadAdminTag(adminUserID)
	cutoff := time.Now().Add(-restoreUploadTTL)
	for _, pat := range []string{"rup-" + tag + "-*.tar.zst", "ruo-" + tag + "-*.json"} {
		matches, _ := filepath.Glob(filepath.Join(restoreUploadDir, pat))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.Remove(m)
			}
		}
	}
}

type restoreUploadOutcome struct {
	Status   string   `json:"status"` // restoring | done | failed
	TS       int64    `json:"ts"`
	Applied  []string `json:"applied,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func restoreUploadOutcomePath(adminUserID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-upload-outcome:" + adminUserID + ":" + uploadID))
	return filepath.Join(restoreUploadDir, "ruo-"+restoreUploadAdminTag(adminUserID)+"-"+hex.EncodeToString(sum[:])+".json")
}

func writeRestoreUploadOutcome(path, status string, applied, warnings []string, detail string) {
	if len(detail) > 600 {
		detail = detail[:600] + "…"
	}
	b, _ := json.Marshal(restoreUploadOutcome{
		Status: status, TS: time.Now().Unix(), Applied: applied, Warnings: warnings, Error: detail,
	})
	writeOutcomeFile(path, b) // atomic swap, shared with the #1323 DB restore
}

func readRestoreUploadOutcome(path string) (*restoreUploadOutcome, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var o restoreUploadOutcome
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	return &o, nil
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
	remaining := maxRestoreUploadBytes - offset
	written, werr := io.Copy(f, io.LimitReader(c.Request.Body, remaining))
	if werr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "write_failed", "detail": werr.Error()})
		return
	}
	// If we wrote exactly up to the cap AND the body still has bytes, the chunk
	// would have been silently truncated — reject instead so no partial archive
	// is ever restored.
	if written == remaining {
		var probe [1]byte
		if n, _ := c.Request.Body.Read(probe[:]); n > 0 {
			_ = os.Remove(path)
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "archive_too_large"})
			return
		}
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
// Destructive → step-up gated. Runs the agent restore + metadata rebuild in a
// DETACHED goroutine (like the #1323 chunked DB restore) and returns 202 — a
// full account restore (extract + rsync home + load DBs) runs for minutes and
// would otherwise die on the axios/nginx/Cloudflare timeout stack, potentially
// half-applied. The client polls restore-upload/status.
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
	evictStaleRestoreUploads(adminID) // reap abandoned partials + old markers

	outcomePath := restoreUploadOutcomePath(adminID, req.UploadID)
	writeRestoreUploadOutcome(outcomePath, "restoring", nil, nil, "")
	go h.runUploadRestore(uploadRestoreArgs{
		path:        path,
		outcomePath: outcomePath,
		username:    req.TargetUsername,
		targetID:    target.ID,
		components:  req.Components,
	})

	c.JSON(http.StatusAccepted, gin.H{"status": "restoring", "upload_id": req.UploadID})
}

type uploadRestoreArgs struct {
	path        string
	outcomePath string
	username    string
	targetID    string
	components  []string
}

// runUploadRestore performs the detached restore: agent apply → metadata rebuild
// (remapped to the target user) → seal the outcome marker → drop the archive.
func (h *backupHandler) runUploadRestore(a uploadRestoreArgs) {
	ctx, cancel := context.WithTimeout(context.Background(), restoreJobTimeout)
	defer cancel()

	raw, err := h.cfg.Agent.Call(ctx, "backup.restore_from_tar", map[string]any{
		"job_id":          ids.NewULID(),
		"tar_path":        a.path,
		"target_username": a.username,
		"components":      a.components,
	})
	if err != nil {
		writeRestoreUploadOutcome(a.outcomePath, "failed", nil, nil, restoreFailureDetail(err))
		_ = os.Remove(a.path) // terminal failure — don't strand a huge tar
		return
	}
	var result struct {
		Applied  []string        `json:"applied"`
		Warnings []string        `json:"warnings"`
		Metadata json.RawMessage `json:"metadata"`
	}
	_ = json.Unmarshal(raw, &result)

	// Rebuild panel DB rows from the backup's metadata bundle, REMAPPED to this
	// box's target user. The bundle carries the SOURCE box's user_id; every child
	// row FKs to it (backupmetadata.Apply). On a cross-server restore that id
	// isn't this box's target user, so rewrite user.id to the resolved target
	// before applying — otherwise the rows attach to a non-existent user.
	metaErrs := h.applyRestoreMetadataForUser(ctx, result.Metadata, a.targetID)
	result.Warnings = append(result.Warnings, metaErrs...)

	// The archive is consumed — drop it so a downloaded backup with real data
	// doesn't linger in the uploads dir.
	_ = os.Remove(a.path)

	writeRestoreUploadOutcome(a.outcomePath, "done", result.Applied, result.Warnings, "")
}

// applyRestoreMetadataForUser rewrites the metadata bundle's user id to targetID
// (so a cross-server bundle's rows attach to THIS box's user) before the shared
// applyRestoreMetadata rebuild.
func (h *backupHandler) applyRestoreMetadataForUser(ctx context.Context, metaRaw json.RawMessage, targetID string) []string {
	if len(metaRaw) == 0 || targetID == "" {
		return h.applyRestoreMetadata(ctx, metaRaw)
	}
	remapped, err := remapMetadataUserID(metaRaw, targetID)
	if err != nil {
		return []string{err.Error()}
	}
	return h.applyRestoreMetadata(ctx, remapped)
}

// remapMetadataUserID rewrites the metadata bundle's user.id to targetID. Every
// child row FKs to user.id in backupmetadata.Apply, so a single rewrite reattaches
// a cross-server bundle to this box's target user. Pure — unit-tested.
func remapMetadataUserID(metaRaw json.RawMessage, targetID string) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		return nil, fmt.Errorf("parse metadata bundle: %w", err)
	}
	if u, ok := m["user"].(map[string]any); ok {
		u["id"] = targetID
	}
	remapped, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("remap metadata user: %w", err)
	}
	return remapped, nil
}

// restoreUploadStatus handles GET /admin/backups/restore-upload/status?upload_id=…
// — polls the detached restore's outcome marker.
func (h *backupHandler) restoreUploadStatus(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	if !uploadIDRE.MatchString(uploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	o, err := readRestoreUploadOutcome(restoreUploadOutcomePath(adminID, uploadID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, o)
}
