package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
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
	uploadIDRE              = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	restoreTargetUsernameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
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
	// GH #1408: annotate whether the bundle's own user already exists (so the UI
	// offers restore-into-existing vs create-from-backup) and whether this
	// server can create it (Packages wired).
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) == nil {
		targetExists := false
		if u, ok := parsed["user"].(map[string]any); ok {
			if uname, _ := u["username"].(string); uname != "" {
				if ex, _ := h.cfg.Users.FindByUsername(c.Request.Context(), uname); ex != nil {
					targetExists = true
				}
			}
		}
		parsed["target_exists"] = targetExists
		parsed["create_supported"] = h.cfg.Packages != nil
		if out, merr := json.Marshal(parsed); merr == nil {
			c.Data(http.StatusOK, "application/json", out)
			return
		}
	}
	c.Data(http.StatusOK, "application/json", raw) // fallback: unparseable → pass through
}

type restoreUploadApplyRequest struct {
	UploadID       string   `json:"upload_id"`
	TargetUsername string   `json:"target_username"`
	Components     []string `json:"components,omitempty"`
	// GH #1408 create-from-manifest: when the target user does not exist yet
	// (fresh-box DR), CreateUser=true creates the account from the bundle
	// before restoring. The username + email come from the bundle (verified
	// against the tar, not trusted from the client); PackageID is the admin's
	// choice (nil = no package = unrestricted resource limits). The account is
	// always non-admin, and its password is regenerated (recover via link).
	CreateUser bool    `json:"create_user,omitempty"`
	PackageID  *string `json:"package_id,omitempty"`
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
	// GH #1408 (reporter feedback): NO step-up for an account restore. It's
	// already admin-only (RequireAdmin), and a recent-auth gate here bounced the
	// admin to re-login mid-apply — a full-page redirect that lost the flow and
	// left the restore un-run with no feedback. Step-up belongs on the (not-yet-
	// built) SYSTEM restore, which changes server config; an account restore is
	// ordinary admin work.
	var req restoreUploadApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil || !uploadIDRE.MatchString(req.UploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if !restoreTargetUsernameRE.MatchString(req.TargetUsername) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target_username"})
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

	target, uerr := h.cfg.Users.FindByUsername(c.Request.Context(), req.TargetUsername)
	userCreated := false
	if uerr != nil || target == nil {
		// GH #1408 create-from-manifest: create the account from the bundle when
		// the admin opted in (fresh-box DR), else keep the historic 404.
		if !req.CreateUser {
			c.JSON(http.StatusNotFound, gin.H{"error": "target_user_not_found", "detail": "user does not exist — enable 'create from backup', or create the user first"})
			return
		}
		newTarget, ok := h.createUserFromBundle(c, path, req.TargetUsername, req.PackageID)
		if !ok {
			return // helper wrote the error response
		}
		target = newTarget
		userCreated = true
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
		userCreated: userCreated,
	})

	c.JSON(http.StatusAccepted, gin.H{"status": "restoring", "upload_id": req.UploadID, "user_created": userCreated})
}

// createUserFromBundle creates the restore-target account from the uploaded
// bundle's OWN user metadata (GH #1408 create-from-manifest) for fresh-box DR
// where the user doesn't exist yet. The username + email are read from the tar
// here (source of truth — never trusted from the client), the account is ALWAYS
// non-admin, its package is the admin's choice (nil = unrestricted), and its
// password is regenerated (the user recovers via a Kratos link — it is never
// displayed). On any error the helper writes the HTTP response and returns
// ok=false.
// createBundleError carries the HTTP shape for a create-from-bundle guard
// failure so the gin wrapper renders it verbatim while the detached full-server
// loop (no gin.Context) can read the reason for its per-user marker line.
type createBundleError struct {
	status int
	code   string
	detail string
}

func (e *createBundleError) Error() string {
	if e.detail != "" {
		return e.code + ": " + e.detail
	}
	return e.code
}

// resolveOrCreateUserFromTar reads the bundle's OWN user straight from the tar
// (agent inspect — never trusted from the client) and creates a NON-ADMIN
// account for it (GH #1408 create-from-manifest). Context-free so the detached
// full-server restore loop can call it per user. Guard failures return a
// *createBundleError; a userops.Create failure is returned verbatim (the gin
// wrapper maps it via userOpsRESTError). The client-supplied targetUsername is
// only a cross-check — home is name-keyed, so it must equal the bundle username.
func (h *backupHandler) resolveOrCreateUserFromTar(ctx context.Context, tarPath, targetUsername string, packageID *string) (*models.User, error) {
	if h.cfg.Packages == nil {
		return nil, &createBundleError{http.StatusNotImplemented, "create_from_bundle_unavailable", "create-from-backup is not enabled on this server"}
	}
	raw, err := h.cfg.Agent.Call(ctx, "backup.inspect_uploaded_tar", map[string]string{"tar_path": tarPath})
	if err != nil {
		return nil, &createBundleError{http.StatusBadGateway, "inspect_failed", restoreFailureDetail(err)}
	}
	var ins struct {
		User struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			IsAdmin  bool   `json:"is_admin"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &ins) != nil || ins.User.Username == "" {
		return nil, &createBundleError{http.StatusBadRequest, "bundle_unreadable", ""}
	}
	if ins.User.Username != targetUsername {
		return nil, &createBundleError{http.StatusBadRequest, "username_mismatch", "restore into the backup's own username (" + ins.User.Username + ")"}
	}
	// SECURITY: never create an admin from a bundle (belt beyond the applyUser
	// refusal — no legitimate account backup is an admin).
	if ins.User.IsAdmin {
		return nil, &createBundleError{http.StatusForbidden, "admin_bundle_refused", "refusing to create an admin account from a backup"}
	}
	if ins.User.Email == "" {
		return nil, &createBundleError{http.StatusBadRequest, "bundle_no_email", "the backup carries no user email — create the user manually, then restore into it"}
	}
	// Don't hijack an existing identity: an email collision is the admin's to
	// resolve (restore into the existing user, or create manually).
	if existing, _ := h.cfg.Users.FindByEmail(ctx, ins.User.Email); existing != nil {
		return nil, &createBundleError{http.StatusConflict, "email_taken", "a user with that email already exists — restore into it instead"}
	}

	pw, perr := randomRestorePassword()
	if perr != nil {
		return nil, &createBundleError{http.StatusInternalServerError, "internal", ""}
	}
	uname := ins.User.Username
	res, cerr := userops.Create(ctx, userops.Deps{
		Users:        h.cfg.Users,
		Packages:     h.cfg.Packages,
		Agent:        h.cfg.Agent,
		KratosClient: h.cfg.KratosClient,
		BcryptCost:   bcrypt.DefaultCost,
		Log:          h.cfg.Log,
	}, userops.CreateInput{
		Email:     ins.User.Email,
		Password:  pw,
		Username:  &uname,
		IsAdmin:   false, // NEVER an admin from a bundle
		PackageID: packageID,
	})
	if cerr != nil {
		return nil, cerr
	}
	return res.User, nil
}

// createUserFromBundle is the gin wrapper over resolveOrCreateUserFromTar for
// the single-account apply handler: on error it writes the HTTP response and
// returns ok=false.
func (h *backupHandler) createUserFromBundle(c *gin.Context, tarPath, targetUsername string, packageID *string) (*models.User, bool) {
	u, err := h.resolveOrCreateUserFromTar(c.Request.Context(), tarPath, targetUsername, packageID)
	if err != nil {
		var ce *createBundleError
		if errors.As(err, &ce) {
			c.JSON(ce.status, gin.H{"error": ce.code, "detail": ce.detail})
		} else {
			userOpsRESTError(c, err) // userops sentinels → HTTP
		}
		return nil, false
	}
	return u, true
}

// randomRestorePassword returns an unguessable password for a create-from-
// manifest account. It is never returned to the caller — the user signs in via
// a Kratos recovery link. The fixed prefix guarantees the char-class mix any
// password policy expects; the suffix is 24 random bytes.
func randomRestorePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "Jb1!" + base64.RawURLEncoding.EncodeToString(b), nil
}

type uploadRestoreArgs struct {
	path        string
	outcomePath string
	username    string
	targetID    string
	userCreated bool
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
		detail := restoreFailureDetail(err)
		if a.userCreated {
			// GH #1408: the account was created before the restore ran — leave it
			// (deleting is the destructive path). A retry re-uploads and restores
			// into the now-existing user, no create needed.
			detail += " — note: the account was created; re-upload and restore into the now-existing user"
		}
		writeRestoreUploadOutcome(a.outcomePath, "failed", nil, nil, detail)
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

	// GH #1408: when we created the account for this DR restore, tell the admin
	// how the user signs in (their password was regenerated, not restored).
	if a.userCreated {
		result.Warnings = append(result.Warnings,
			"Account "+a.username+" was created from the backup with a regenerated password — send a recovery link: jabali user password "+a.username+" --link")
	}

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
