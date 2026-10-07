package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/uploadedbackups"
)

// uploaded_backups.go — GH #1993. An account backup an admin uploads from
// another server stays on this one instead of being deleted after the restore:
// a failed restore of a multi-GB archive no longer means uploading it again.
//
// After the chunked upload (restore-upload), the admin registers it with a
// retention choice: the agent checks it is a Jabali account backup, the file
// moves from the admin's staging path to uploadedbackups.Path(id), and a row
// lists it in Backups. From there it is restored (as often as needed) or
// deleted; uploadedbackups.Sweep removes it when its retention ends.

// maxRestoreResultBytes bounds the stored restore report.
const maxRestoreResultBytes = 60000

var archiveComponentRE = regexp.MustCompile(`^[a-z_]{1,32}$`)

func (h *backupHandler) registerUploadedBackupRoutes(admin *gin.RouterGroup) {
	admin.GET("/uploaded-backups", h.listUploadedBackups)
	admin.POST("/uploaded-backups", h.registerUploadedBackup)
	admin.GET("/uploaded-backups/:id", h.getUploadedBackup)
	admin.POST("/uploaded-backups/:id/restore", h.restoreUploadedBackup)
	admin.DELETE("/uploaded-backups/:id", h.deleteUploadedBackup)
}

// uploadedRestoreResult is a restore's report, stored as the row's
// restore_result.
type uploadedRestoreResult struct {
	Applied  []string `json:"applied,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// uploadedBackupView is an uploaded backup as the API returns it.
type uploadedBackupView struct {
	models.UploadedBackup
	Components    []string               `json:"components"`
	FilePresent   bool                   `json:"file_present"`
	RestoreResult *uploadedRestoreResult `json:"restore_result,omitempty"`
	// TargetExists says whether the account the archive was taken from exists
	// here (detail and register only); CreateSupported whether the restore can
	// create it.
	TargetExists    *bool `json:"target_exists,omitempty"`
	CreateSupported bool  `json:"create_supported"`
	// RestoreProgress is the running restore's progress by step (GH #1993).
	RestoreProgress *restoreProgress `json:"restore_progress,omitempty"`
}

// uploadedRestoreProgressKey keys the progress of a restore of an uploaded
// backup.
func uploadedRestoreProgressKey(id string) string { return "uploaded-backup:" + id }

func (h *backupHandler) uploadedView(ctx context.Context, b *models.UploadedBackup, detail bool) uploadedBackupView {
	v := uploadedBackupView{UploadedBackup: *b, Components: []string{}, CreateSupported: h.cfg.Packages != nil}
	if b.Components != "" {
		v.Components = strings.Split(b.Components, ",")
	}
	if fi, err := os.Lstat(uploadedbackups.Path(b.ID)); err == nil && fi.Mode().IsRegular() {
		v.FilePresent = true
	}
	if b.RestoreStatus == models.UploadedBackupRestoring {
		v.RestoreProgress = uploadRestoreProgressFor(uploadedRestoreProgressKey(b.ID))
	}
	if b.RestoreResult != nil && *b.RestoreResult != "" {
		var r uploadedRestoreResult
		if json.Unmarshal([]byte(*b.RestoreResult), &r) == nil {
			v.RestoreResult = &r
		}
	}
	if detail {
		exists := false
		if u, err := h.cfg.Users.FindByUsername(ctx, b.AccountUsername); err == nil && u != nil {
			exists = true
		}
		v.TargetExists = &exists
	}
	return v
}

// uploadedBackupParam loads the row named by :id, or writes 404.
func (h *backupHandler) uploadedBackupParam(c *gin.Context) (*models.UploadedBackup, bool) {
	id := c.Param("id")
	if !uploadedbackups.ValidID(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, false
	}
	b, err := h.cfg.UploadedBackups.FindByID(c.Request.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, false
	}
	if err != nil {
		h.cfg.logErr("find uploaded backup", err, "id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup_failed"})
		return nil, false
	}
	return b, true
}

func (h *backupHandler) listUploadedBackups(c *gin.Context) {
	page, pageSize, opts := parseListOptions(c, 25, 100)
	rows, total, err := h.cfg.UploadedBackups.List(c.Request.Context(), opts)
	if err != nil {
		h.cfg.logErr("list uploaded backups", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	out := make([]uploadedBackupView, 0, len(rows))
	for i := range rows {
		out = append(out, h.uploadedView(c.Request.Context(), &rows[i], false))
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": total, "page": page, "page_size": pageSize})
}

func (h *backupHandler) getUploadedBackup(c *gin.Context) {
	b, ok := h.uploadedBackupParam(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.uploadedView(c.Request.Context(), b, true)})
}

type registerUploadedBackupRequest struct {
	UploadID  string `json:"upload_id"`
	Retention string `json:"retention"`
	FileName  string `json:"file_name"`
}

// displayFileName keeps the base name of an uploaded file name, without
// control characters, for display only (it never reaches a path).
func displayFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "." || name == "/" {
		return ""
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	return name
}

// agentReason is an agent error's own message, or a generic one.
func agentReason(err error, generic string) string {
	var ae *agentwire.AgentError
	if errors.As(err, &ae) && ae.Message != "" {
		return ae.Message
	}
	return generic
}

// registerUploadedBackup handles POST /admin/uploaded-backups: keep the
// calling admin's finished upload as an uploaded backup.
func (h *backupHandler) registerUploadedBackup(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	var req registerUploadedBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil || !uploadIDRE.MatchString(req.UploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if req.Retention == "" {
		req.Retention = models.UploadedBackupKeep
	}
	if !uploadedbackups.ValidRetention(req.Retention) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_retention", "detail": "retention must be keep, keep_7_days or delete_after_restore"})
		return
	}
	// The staged file is derived from the calling admin, so one admin can't
	// keep another's upload.
	staged := restoreUploadPath(adminID, req.UploadID)
	fi, err := os.Lstat(staged)
	if err != nil || !fi.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "backup.inspect_uploaded_tar", map[string]string{"tar_path": staged})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_an_account_backup",
			"detail": agentReason(err, "the file could not be read as a Jabali account backup")})
		return
	}
	var ins struct {
		User struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
		Components []string `json:"components"`
	}
	if json.Unmarshal(raw, &ins) != nil || !restoreTargetUsernameRE.MatchString(ins.User.Username) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_an_account_backup",
			"detail": "the file is not a Jabali account backup this server can restore"})
		return
	}
	var comps []string
	for _, comp := range ins.Components {
		if archiveComponentRE.MatchString(comp) && len(strings.Join(append(comps, comp), ",")) <= 255 {
			comps = append(comps, comp)
		}
	}
	email := ins.User.Email
	if len(email) > 255 {
		email = ""
	}

	if err := os.MkdirAll(uploadedbackups.Dir, 0o750); err != nil {
		h.cfg.logErr("create uploaded backups dir", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "keep_failed"})
		return
	}
	now := time.Now().UTC()
	row := &models.UploadedBackup{
		ID:              ids.NewULID(),
		FileName:        displayFileName(req.FileName),
		SizeBytes:       fi.Size(),
		AccountUsername: ins.User.Username,
		AccountEmail:    email,
		Components:      strings.Join(comps, ","),
		Retention:       req.Retention,
		ExpiresAt:       uploadedbackups.ExpiresAt(req.Retention, now),
		UploadedBy:      adminID,
		CreatedAt:       now,
	}
	kept := uploadedbackups.Path(row.ID)
	if err := os.Rename(staged, kept); err != nil {
		h.cfg.logErr("keep uploaded backup", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "keep_failed"})
		return
	}
	if err := h.cfg.UploadedBackups.Create(c.Request.Context(), row); err != nil {
		// Put the upload back where the admin can register it again.
		_ = os.Rename(kept, staged)
		h.cfg.logErr("store uploaded backup", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "keep_failed"})
		return
	}
	c.Set("audit_target", row.AccountUsername)
	c.Set("audit_target_type", "user")
	c.JSON(http.StatusCreated, gin.H{"data": h.uploadedView(c.Request.Context(), row, true)})
}

type restoreUploadedBackupRequest struct {
	TargetUsername string   `json:"target_username"`
	Components     []string `json:"components,omitempty"`
	CreateUser     bool     `json:"create_user,omitempty"`
	PackageID      *string  `json:"package_id,omitempty"`
	// Overwrite: see restoreUploadApplyRequest.Overwrite.
	Overwrite bool `json:"overwrite,omitempty"`
}

// restoreUploadedBackup handles POST /admin/uploaded-backups/:id/restore. It
// runs the restore detached (like restore-upload/apply) and returns 202; the
// row's restore_status and restore_result report it to any admin.
func (h *backupHandler) restoreUploadedBackup(c *gin.Context) {
	b, ok := h.uploadedBackupParam(c)
	if !ok {
		return
	}
	var req restoreUploadedBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if !restoreTargetUsernameRE.MatchString(req.TargetUsername) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target_username"})
		return
	}
	path := uploadedbackups.Path(b.ID)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		c.JSON(http.StatusGone, gin.H{"error": "archive_missing", "detail": "the archive file is no longer on this server; delete this entry and upload the backup again"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	if !agentHasCapability(c.Request.Context(), h.cfg.Agent, capRestoreUploadConfinement) {
		c.JSON(http.StatusConflict, gin.H{"error": "agent_update_required", "detail": agentUpdateRequiredDetail})
		return
	}
	if keepExistingRefused(c, h.cfg.Agent, req.Overwrite) {
		return
	}
	now := time.Now().UTC()
	switch err := h.cfg.UploadedBackups.ClaimRestore(c.Request.Context(), b.ID, req.TargetUsername, now, uploadedbackups.StaleBefore(now)); {
	case errors.Is(err, repository.ErrUploadedBackupBusy):
		c.JSON(http.StatusConflict, gin.H{"error": "restore_in_progress", "detail": "this backup is being restored; wait for that restore to finish"})
		return
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	case err != nil:
		h.cfg.logErr("claim uploaded backup restore", err, "id", b.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "restore_failed"})
		return
	}

	target, uerr := h.cfg.Users.FindByUsername(c.Request.Context(), req.TargetUsername)
	userCreated := false
	if uerr != nil || target == nil {
		if !req.CreateUser {
			h.finishUploadedRestore(b, models.UploadedBackupFailed, uploadedRestoreResult{Error: "the account does not exist"})
			c.JSON(http.StatusNotFound, gin.H{"error": "target_user_not_found", "detail": "user does not exist — enable 'create from backup', or create the user first"})
			return
		}
		newTarget, ok := h.createUserFromBundle(c, path, req.TargetUsername, req.PackageID)
		if !ok {
			h.finishUploadedRestore(b, models.UploadedBackupFailed, uploadedRestoreResult{Error: "the account could not be created from the backup"})
			return // helper wrote the error response
		}
		target, userCreated = newTarget, true
	}

	c.Set("audit_target", req.TargetUsername)
	c.Set("audit_target_type", "user")
	go h.runUploadedBackupRestore(b, path, req.TargetUsername, target.ID, req.Components, userCreated, req.Overwrite)
	c.JSON(http.StatusAccepted, gin.H{"status": models.UploadedBackupRestoring, "id": b.ID, "user_created": userCreated})
}

// runUploadedBackupRestore is the detached restore of an uploaded backup. The
// archive stays, unless its retention is delete_after_restore and the restore
// succeeded.
func (h *backupHandler) runUploadedBackupRestore(b *models.UploadedBackup, path, username, targetID string, components []string, userCreated, overwrite bool) {
	ctx, cancel := context.WithTimeout(context.Background(), restoreJobTimeout)
	defer cancel()

	report, done := progressReporter(uploadedRestoreProgressKey(b.ID))
	defer done()
	res, err := h.restoreUploadedAccount(ctx, path, username, targetID, components, overwrite, report)
	result := uploadedRestoreResult{Applied: res.Applied, Warnings: append(res.Warnings, res.MetadataErrors...)}
	status := models.UploadedBackupDone
	if err != nil {
		status = models.UploadedBackupFailed
		result.Error = err.Error()
		if userCreated {
			result.Error += " — note: the account was created; restore into it again"
		}
	} else if userCreated {
		result.Warnings = append(result.Warnings,
			"Account "+username+" was created from the backup with a regenerated password — send a recovery link: jabali user password "+username+" --link")
	}
	// delete_after_restore: the archive goes once a restore succeeded (a
	// failed one keeps it for a retry). The row stays a day so the report can
	// be read, then the sweeper removes it.
	var expires *time.Time
	if status == models.UploadedBackupDone && b.Retention == models.UploadedBackupDeleteAfterRestore {
		if rerr := uploadedbackups.Remove(b.ID); rerr != nil {
			h.cfg.logErr("remove restored uploaded backup archive", rerr, "id", b.ID)
		} else {
			at := time.Now().UTC().Add(restoredRowKeptFor)
			expires = &at
			result.Warnings = append(result.Warnings, "The uploaded backup was deleted after this restore, as chosen when it was uploaded")
		}
	}
	h.finishUploadedRestoreUntil(b, status, result, expires)
}

// restoredRowKeptFor is how long the row of a delete_after_restore backup
// stays after its restore succeeded, so the report can be read.
const restoredRowKeptFor = 24 * time.Hour

// finishUploadedRestore stores a restore's outcome on its row.
func (h *backupHandler) finishUploadedRestore(b *models.UploadedBackup, status string, result uploadedRestoreResult) {
	h.finishUploadedRestoreUntil(b, status, result, nil)
}

// finishUploadedRestoreUntil is finishUploadedRestore that also sets when the
// row expires (nil leaves it).
func (h *backupHandler) finishUploadedRestoreUntil(b *models.UploadedBackup, status string, result uploadedRestoreResult, expiresAt *time.Time) {
	if err := h.cfg.UploadedBackups.FinishRestore(context.Background(), b.ID, status, encodeRestoreResult(result), time.Now().UTC(), expiresAt); err != nil {
		h.cfg.logErr("record uploaded backup restore", err, "id", b.ID)
	}
}

// encodeRestoreResult encodes r, dropping warnings from the end until it fits
// the stored size.
func encodeRestoreResult(r uploadedRestoreResult) string {
	out, _ := json.Marshal(r)
	dropped := 0
	for len(out) > maxRestoreResultBytes && len(r.Warnings) > 0 {
		r.Warnings = r.Warnings[:len(r.Warnings)-1]
		dropped++
		trimmed := r
		trimmed.Warnings = append(append([]string{}, r.Warnings...), "… "+strconv.Itoa(dropped)+" more warnings not shown")
		out, _ = json.Marshal(trimmed)
	}
	if len(out) > maxRestoreResultBytes {
		out, _ = json.Marshal(uploadedRestoreResult{Error: "the restore report was too long to keep"})
	}
	return string(out)
}

// deleteUploadedBackup handles DELETE /admin/uploaded-backups/:id.
func (h *backupHandler) deleteUploadedBackup(c *gin.Context) {
	id := c.Param("id")
	if !uploadedbackups.ValidID(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	now := time.Now().UTC()
	switch err := h.cfg.UploadedBackups.DeleteIfIdle(c.Request.Context(), id, uploadedbackups.StaleBefore(now)); {
	case errors.Is(err, repository.ErrUploadedBackupBusy):
		c.JSON(http.StatusConflict, gin.H{"error": "restore_in_progress", "detail": "this backup is being restored; delete it once that restore finishes"})
		return
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	case err != nil:
		h.cfg.logErr("delete uploaded backup", err, "id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed"})
		return
	}
	// The row is gone; a file that won't go is swept as an orphan later.
	if err := uploadedbackups.Remove(id); err != nil {
		h.cfg.logErr("remove uploaded backup archive", err, "id", id)
	}
	c.Set("audit_target", id)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
