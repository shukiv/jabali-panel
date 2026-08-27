package api

// GH #1323 — chunked "Restore from file" upload with async finalize.
//
// The single-request restore 413s at Cloudflare (Free plan caps request bodies at
// 100 MB) before it reaches the panel. The File Manager already uploads >100 MB
// files as sequential 10 MB chunks; this brings the same to database restore.
//
// Each chunk is a plain octet-stream POST appended at `offset` into a
// PANEL-CONTROLLED staging file (never a tenant-influenced path). The staging path
// is derived from the AUTHENTICATED user id + the client upload_id, so one tenant
// can never target another's staging file even if they learn the id.
//
// FINALIZE IS ASYNC. A large dump can restore for minutes; holding the final
// chunk's connection through the load would trip Cloudflare's ~100 s origin
// timeout (524) even though every request is now chunk-sized. So `final=1`
// assembles the dump, kicks the restore on a DETACHED goroutine, records the
// outcome to a marker keyed by upload_id, and returns 202 immediately — the client
// polls GET /restore-status until done/failed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// restoreRoot is where restore dumps (and, under staging/, partial chunked
// uploads + outcome markers) are written. A var, not a const, so tests can point
// it at t.TempDir() — production always uses the fixed path. Shared with the
// single-upload restore.
var restoreRoot = "/var/lib/jabali/restore"

// restoreStagingDir holds partial chunked-restore uploads + outcome markers until
// they age out, under the restore root so it shares the service partition.
func restoreStagingDir() string { return filepath.Join(restoreRoot, "staging") }

const (
	// maxInFlightRestoreUploads caps concurrent partial uploads per user so a
	// tenant can't fill the service partition with never-finalized chunks.
	maxInFlightRestoreUploads = 2
	// restoreStagingTTL evicts abandoned partials + terminal outcome markers so
	// two dead uploads can't permanently 429-lock a user out of chunked restore.
	restoreStagingTTL = 24 * time.Hour
)

func restoreUserTag(userID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-user:" + userID))
	return hex.EncodeToString(sum[:])[:16]
}

// restoreChunkStagingPath derives the staging path from the AUTHENTICATED user +
// the client upload_id (Gitea #426 pattern): the path depends on the server-side
// userID, so a tenant can never write into or read another tenant's staging file.
func restoreChunkStagingPath(userID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-restore:" + userID + ":" + uploadID))
	return filepath.Join(restoreStagingDir(), "ru-"+restoreUserTag(userID)+"-"+hex.EncodeToString(sum[:])+".part")
}

// restoreOutcomePath is the async-restore result marker for one upload.
func restoreOutcomePath(userID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-outcome:" + userID + ":" + uploadID))
	return filepath.Join(restoreStagingDir(), "ro-"+restoreUserTag(userID)+"-"+hex.EncodeToString(sum[:])+".json")
}

func globUserRestoreStaging(userID string) []string {
	m, _ := filepath.Glob(filepath.Join(restoreStagingDir(), "ru-"+restoreUserTag(userID)+"-*.part"))
	return m
}

// evictStaleRestoreStaging removes this user's partials + outcome markers older
// than restoreStagingTTL, so abandoned uploads can't permanently occupy an
// in-flight slot or leak disk. Best-effort.
func evictStaleRestoreStaging(userID string) {
	patterns := []string{
		filepath.Join(restoreStagingDir(), "ru-"+restoreUserTag(userID)+"-*.part"),
		filepath.Join(restoreStagingDir(), "ro-"+restoreUserTag(userID)+"-*.json"),
	}
	cutoff := time.Now().Add(-restoreStagingTTL)
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.Remove(m)
			}
		}
	}
}

type restoreOutcome struct {
	Status string `json:"status"` // "restoring" | "done" | "failed"
	TS     int64  `json:"ts"`
}

func writeRestoreOutcome(path, status string) {
	b, _ := json.Marshal(restoreOutcome{Status: status, TS: time.Now().Unix()})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, path) // atomic swap so a poll never sees a torn write
	}
}

// loadRestoreDatabase loads the target DB and enforces the same authorization as
// the single-upload restore (admin: any; user: own). On failure it writes the
// response and returns ok=false.
func (h *databaseHandler) loadRestoreDatabase(c *gin.Context) (*models.Database, bool) {
	d, err := h.cfg.Databases.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return nil, false
	}
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	if !claims.IsAdmin && d.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	return d, true
}

// clearRequestDeadlines lifts the http.Server's 30 s Read/Write deadlines
// (serve.go) for this request. Every chunk body read and the finalize response
// must not be guillotined — a slow uplink can take >30 s to send a 10 MB chunk.
func clearRequestDeadlines(c *gin.Context) {
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.DebugContext(c.Request.Context(), "restoreChunk: clear read deadline failed", "err", err)
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.DebugContext(c.Request.Context(), "restoreChunk: clear write deadline failed", "err", err)
	}
}

// restoreChunkStatus handles GET /databases/:id/restore-chunk-status?upload_id=…
// It returns how many bytes of the staging file have landed, so the client can
// resume an interrupted upload from the right offset.
func (h *databaseHandler) restoreChunkStatus(c *gin.Context) {
	if _, ok := h.loadRestoreDatabase(c); !ok {
		return
	}
	claims := ginctx.Claims(c)
	uploadID := c.Query("upload_id")
	if uploadID == "" || strings.ContainsAny(uploadID, "/\\.") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	var written int64
	if fi, err := os.Stat(restoreChunkStagingPath(claims.UserID, uploadID)); err == nil && fi.Mode().IsRegular() {
		written = fi.Size()
	}
	c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "written": written})
}

// restoreStatus handles GET /databases/:id/restore-status?upload_id=… — the
// async-finalize poll target. Returns {status: restoring|done|failed}.
func (h *databaseHandler) restoreStatus(c *gin.Context) {
	if _, ok := h.loadRestoreDatabase(c); !ok {
		return
	}
	claims := ginctx.Claims(c)
	uploadID := c.Query("upload_id")
	if uploadID == "" || strings.ContainsAny(uploadID, "/\\.") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	b, err := os.ReadFile(restoreOutcomePath(claims.UserID, uploadID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown_upload"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	var o restoreOutcome
	if json.Unmarshal(b, &o) != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// A "restoring" marker older than the restore ceiling means the worker died
	// (panel restart) — report failed so the client stops polling forever.
	if o.Status == "restoring" && time.Since(time.Unix(o.TS, 0)) > restoreAgentTimeout {
		o.Status = "failed"
	}
	c.JSON(http.StatusOK, gin.H{"status": o.Status})
}

// restoreChunk handles POST /databases/:id/restore-chunk?upload_id=…&offset=…[&final=1].
// The body is a raw octet-stream chunk appended at `offset`. On `final=1` the
// assembled dump is restored ASYNCHRONOUSLY (see file header).
func (h *databaseHandler) restoreChunk(c *gin.Context) {
	d, ok := h.loadRestoreDatabase(c)
	if !ok {
		return
	}
	claims := ginctx.Claims(c)
	// A slow uplink can take >30 s to stream a 10 MB chunk; lift the server's
	// Read/Write deadlines for the whole request so a chunk (or the finalize) is
	// never guillotined mid-body.
	clearRequestDeadlines(c)

	uploadID := c.Query("upload_id")
	offsetStr := c.Query("offset")
	isFinal := c.Query("final") == "1"
	if uploadID == "" || offsetStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_upload_params"})
		return
	}
	if strings.ContainsAny(uploadID, "/\\.") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	var offset int64
	if _, err := fmt.Sscanf(offsetStr, "%d", &offset); err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_offset"})
		return
	}
	if err := createDir(restoreStagingDir()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	tmpPath := restoreChunkStagingPath(claims.UserID, uploadID)
	maxUploadSize := h.resolveMaxRestoreBytes(c.Request.Context())

	// New upload (offset 0, no staging yet): evict this user's stale partials,
	// then enforce the per-user in-flight cap.
	if offset == 0 {
		if _, statErr := os.Stat(tmpPath); errors.Is(statErr, os.ErrNotExist) {
			evictStaleRestoreStaging(claims.UserID)
			if len(globUserRestoreStaging(claims.UserID)) >= maxInFlightRestoreUploads {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_uploads"})
				return
			}
		}
	}

	// O_EXCL on the first chunk (fresh session); later chunks require the file to
	// exist and the offset to equal its current size — no holes, no mid-file
	// overwrite, no cross-session injection.
	openFlags := os.O_WRONLY
	if offset == 0 {
		openFlags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(tmpPath, openFlags, 0o600)
	if err != nil {
		if offset == 0 && errors.Is(err, os.ErrExist) {
			f, err = os.OpenFile(tmpPath, os.O_WRONLY, 0o600) // idempotent first-chunk retry
		}
		if err != nil {
			if offset > 0 && errors.Is(err, os.ErrNotExist) {
				c.JSON(http.StatusConflict, gin.H{"error": "upload_not_found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	}
	fi, statErr := f.Stat()
	if statErr != nil {
		f.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if offset != fi.Size() {
		f.Close()
		c.JSON(http.StatusConflict, gin.H{"error": "bad_offset", "expected": fi.Size()})
		return
	}
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Cap the assembled upload at the admin-configured restore limit. LimitReader
	// bounds the write; the post-write size check rejects an over-cap total. A
	// copy error truncates back to the last good size (resumable) rather than
	// deleting the whole staged upload — a slow-link blip must not lose it.
	written, copyErr := io.Copy(f, io.LimitReader(c.Request.Body, maxUploadSize-offset+1))
	if cerr := f.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Truncate(tmpPath, offset)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if offset+written > maxUploadSize {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error":    "file_too_large",
			"max_size": fmt.Sprintf("%dMB", maxUploadSize/(1024*1024)),
			"detail": "Upload exceeds the panel's restore cap (Server Settings → Uploads). " +
				"A larger request may also be blocked upstream by nginx or Cloudflare.",
		})
		return
	}

	if !isFinal {
		c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "written": written})
		return
	}

	// Final chunk. Need the agent to actually restore.
	if h.cfg.Agent == nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err := createDir(restoreRoot); err != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	restorePath := filepath.Join(restoreRoot, ids.NewULID()+".sql")
	if err := os.Rename(tmpPath, restorePath); err != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Async: record the outcome and run the restore detached from this request so
	// the 202 returns immediately (Cloudflare's ~100 s origin timeout can't span a
	// multi-minute restore). The client polls restore-status. Snapshot everything
	// the goroutine needs — the request is done once we respond.
	outcomePath := restoreOutcomePath(claims.UserID, uploadID)
	writeRestoreOutcome(outcomePath, "restoring")
	go func() {
		// context.Background(): fully detached; doDatabaseRestore applies its own
		// restoreAgentTimeout ceiling.
		if err := h.doDatabaseRestore(context.Background(), d, restorePath); err != nil {
			slog.Error("chunked restore failed", "db_id", d.ID, "err", err)
			writeRestoreOutcome(outcomePath, "failed")
			return
		}
		writeRestoreOutcome(outcomePath, "done")
	}()
	c.JSON(http.StatusAccepted, gin.H{"upload_id": uploadID, "status": "restoring"})
}
