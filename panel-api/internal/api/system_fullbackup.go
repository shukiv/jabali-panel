package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// system_fullbackup.go — GH #1408 / #502. Package a whole Full Server backup RUN
// (system + one job per user, sharing a run_id) into ONE downloadable container,
// so an operator can move an entire server in a single file.
//
// Packaging materializes every job and can take many minutes on a big server, so
// it's a detached JOB (like the system backup itself): POST .../package returns
// 202, the agent packs in the background (system.fullbackup.pack), the client
// polls .../package-status, then GETs .../download to stream the finished
// container. The inner per-user archives are byte-identical to a normal account
// download, so the (phase 2) restore feeds each straight into restore_from_tar.

const fullBackupContainerDir = "/var/lib/jabali-backups/downloads"

func fullBackupMarkerPath(runID string) string {
	return filepath.Join(restoreUploadDir, "fullpkg-"+runID+".json")
}

type fullBackupOutcome struct {
	Status  string   `json:"status"` // packaging | done | failed
	TS      int64    `json:"ts"`
	Path    string   `json:"path,omitempty"`
	Bytes   int64    `json:"bytes,omitempty"`
	Packed  []string `json:"packed,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func writeFullBackupOutcome(path string, o fullBackupOutcome) {
	o.TS = time.Now().Unix()
	if len(o.Error) > 600 {
		o.Error = o.Error[:600] + "…"
	}
	b, _ := json.Marshal(o)
	writeOutcomeFile(path, b) // atomic swap, shared with #1323/#1408
}

// fullBackupPackage handles POST /admin/system/full-backup/:run_id/package.
func (h *backupHandler) fullBackupPackage(c *gin.Context) {
	runID := c.Param("run_id")
	if !uploadIDRE.MatchString(runID) { // ULID also satisfies this format
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_run_id"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	jobs, err := h.cfg.Jobs.ListByRun(c.Request.Context(), runID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db_list"})
		return
	}

	// Build the pack list: each SUCCEEDED/PARTIAL job that has a snapshot, with a
	// stable label ("system" or "users/<username>"). All jobs in a run share one
	// destination, so resolve it once.
	type packItem struct{ jobID, label string }
	var items []packItem
	var dest *models.BackupDestination
	var destParams map[string]any
	for i := range jobs {
		j := &jobs[i]
		if j.Status != models.BackupJobStatusSucceeded && j.Status != models.BackupJobStatusPartial {
			continue
		}
		if j.SnapshotID == "" {
			continue
		}
		if dest == nil {
			dest, destParams = materializeDest(c.Request.Context(), h.cfg.Destinations, j)
		}
		label := "system"
		if j.UserID != "system" {
			u, uerr := h.cfg.Users.FindByID(c.Request.Context(), j.UserID)
			if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
				continue // can't label a user job without its username
			}
			label = "users/" + *u.Username
		}
		items = append(items, packItem{jobID: j.ID, label: label})
	}
	if len(items) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "nothing_to_package", "detail": "run has no completed backups"})
		return
	}

	c.Set("audit_target", runID)
	c.Set("audit_target_type", "backup_run")

	marker := fullBackupMarkerPath(runID)
	writeFullBackupOutcome(marker, fullBackupOutcome{Status: "packaging"})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		runPack := func(pwFile string) error {
			wire := make([]map[string]any, 0, len(items))
			for _, it := range items {
				m := map[string]any{"job_id": it.jobID, "label": it.label}
				for k, v := range destParams {
					m[k] = v
				}
				if pwFile != "" {
					m["password_file"] = pwFile
				}
				wire = append(wire, m)
			}
			raw, cerr := h.cfg.Agent.Call(ctx, "system.fullbackup.pack",
				map[string]any{"run_id": runID, "jobs": wire})
			if cerr != nil {
				writeFullBackupOutcome(marker, fullBackupOutcome{Status: "failed", Error: restoreFailureDetail(cerr)})
				return nil
			}
			var res struct {
				Path    string   `json:"path"`
				Bytes   int64    `json:"bytes"`
				Packed  []string `json:"packed"`
				Skipped []string `json:"skipped"`
			}
			_ = json.Unmarshal(raw, &res)
			writeFullBackupOutcome(marker, fullBackupOutcome{
				Status: "done", Path: res.Path, Bytes: res.Bytes, Packed: res.Packed, Skipped: res.Skipped,
			})
			return nil
		}
		if dest != nil {
			_ = backupwrapperhelpers.WithOptionalDestPassword(ctx, dest, h.cfg.Agent, h.cfg.SSOKey, runPack)
		} else {
			_ = runPack("")
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{"status": "packaging", "run_id": runID})
}

// fullBackupPackageStatus handles GET /admin/system/full-backup/:run_id/package-status.
func (h *backupHandler) fullBackupPackageStatus(c *gin.Context) {
	runID := c.Param("run_id")
	if !uploadIDRE.MatchString(runID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_run_id"})
		return
	}
	b, err := os.ReadFile(fullBackupMarkerPath(runID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.Data(http.StatusOK, "application/json", b)
}

// fullBackupDownload handles GET /admin/system/full-backup/:run_id/download —
// streams the packaged container file the agent built.
func (h *backupHandler) fullBackupDownload(c *gin.Context) {
	runID := c.Param("run_id")
	if !uploadIDRE.MatchString(runID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_run_id"})
		return
	}
	b, err := os.ReadFile(fullBackupMarkerPath(runID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_packaged"})
		return
	}
	var o fullBackupOutcome
	if json.Unmarshal(b, &o) != nil || o.Status != "done" || o.Path == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "not_ready", "status": o.Status})
		return
	}
	// The container is under the agent's downloads dir; confine to it so a
	// tampered marker can't point the stream at an arbitrary file.
	clean := filepath.Clean(o.Path)
	if filepath.Dir(clean) != fullBackupContainerDir {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad_container_path"})
		return
	}
	f, ferr := os.Open(clean)
	if ferr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "container_missing"})
		return
	}
	defer f.Close()
	fi, _ := f.Stat()

	// Multi-GB stream: clear the 30s write deadline (nginx allows an hour).
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
	c.Header("Content-Type", "application/x-tar")
	c.Header("Content-Disposition", contentDisposition("attachment", "full-server-backup-"+runID+".tar"))
	if fi != nil {
		c.Header("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	if _, err := io.Copy(c.Writer, f); err != nil && h.cfg.Log != nil {
		h.cfg.Log.Warn("full-backup stream failed", "err", err, "run_id", runID)
	}
}

// --- Restore from an uploaded full-server container (phase 2) ---
//
// The container is uploaded via the existing chunked endpoint
// (POST /admin/backups/restore-upload), so it reassembles at the same
// restoreUploadPath. These endpoints inspect it and fan the restore out over the
// selected users, reusing the per-account restore engine.

func fullRestoreMarkerPath(adminID, uploadID string) string {
	return filepath.Join(restoreUploadDir, "fullrst-"+restoreUploadAdminTag(adminID)+"-"+uploadID+".json")
}

type fullRestoreInspectRequest struct {
	UploadID string `json:"upload_id"`
}

// fullRestoreInspect handles POST /admin/system/full-restore-upload/inspect.
func (h *backupHandler) fullRestoreInspect(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	var req fullRestoreInspectRequest
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
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "system.fullbackup.inspect_uploaded",
		map[string]string{"tar_path": path})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	// GH #1408 slice 2: annotate which container users already exist (so the UI
	// can offer create-missing) and whether the server can create them.
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) == nil {
		if us, ok := parsed["users"].([]any); ok {
			status := make([]map[string]any, 0, len(us))
			for _, x := range us {
				uname, _ := x.(string)
				exists := false
				if uname != "" {
					if ex, _ := h.cfg.Users.FindByUsername(c.Request.Context(), uname); ex != nil {
						exists = true
					}
				}
				status = append(status, map[string]any{"username": uname, "exists": exists})
			}
			parsed["user_status"] = status
		}
		parsed["create_supported"] = h.cfg.Packages != nil
		if out, merr := json.Marshal(parsed); merr == nil {
			c.Data(http.StatusOK, "application/json", out)
			return
		}
	}
	c.Data(http.StatusOK, "application/json", raw)
}

type fullRestoreApplyRequest struct {
	UploadID      string   `json:"upload_id"`
	Usernames     []string `json:"usernames"`
	IncludeSystem bool     `json:"include_system"`
	// GH #1408 slice 2 create-from-manifest: create any selected user that isn't
	// on this box yet (fresh-box DR), from that user's own inner-tar metadata.
	// One PackageID is applied to every created account (v1). Non-admin, password
	// regenerated — same guards as the single-account create.
	CreateMissing bool    `json:"create_missing,omitempty"`
	PackageID     *string `json:"package_id,omitempty"`
}

// fullRestoreApply handles POST /admin/system/full-restore-upload/apply — restores
// the selected users from the uploaded container. Detached job (it can restore
// many accounts) → 202 + status poll.
func (h *backupHandler) fullRestoreApply(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	var req fullRestoreApplyRequest
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
	c.Set("audit_target", req.UploadID)
	c.Set("audit_target_type", "backup_run")

	marker := fullRestoreMarkerPath(adminID, req.UploadID)
	writeFullBackupOutcome(marker, fullBackupOutcome{Status: "restoring"})

	go h.runFullRestore(path, marker, req)

	c.JSON(http.StatusAccepted, gin.H{"status": "restoring", "upload_id": req.UploadID})
}

// runFullRestore is the detached full-server restore loop (GH #1408 slice 2).
// The PANEL drives it: extract the container once (agent), then per selected
// user resolve-or-create the account and restore its inner tar via the SAME
// per-account path (backup.restore_from_tar + panel metadata rebuild),
// reporting per-user progress. Panel-driven so it reuses create-from-manifest,
// rebuilds full panel state, and works on any prior container (identity is read
// from each inner tar). The extraction stage is always cleaned up.
func (h *backupHandler) runFullRestore(containerPath, marker string, req fullRestoreApplyRequest) {
	// Scale the ceiling with the work: one account restore fit in ~an hour, a
	// whole box does not. Size from the explicit selection when given, else the
	// "all users" case gets a generous fixed ceiling.
	budget := 6 * time.Hour
	if n := len(req.Usernames); n > 0 {
		budget = 20*time.Minute + time.Duration(n)*40*time.Minute
		if budget > 6*time.Hour {
			budget = 6 * time.Hour
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	raw, cerr := h.cfg.Agent.Call(ctx, "system.fullbackup.extract_uploaded", map[string]any{"tar_path": containerPath})
	if cerr != nil {
		detail := restoreFailureDetail(cerr)
		var ae *agentwire.AgentError
		if errors.As(cerr, &ae) && ae.Code == agent.CodeUnknownCommand {
			// Fail closed on an old agent — never fall back to the legacy
			// restore_uploaded, which silently produces the no-metadata behaviour
			// this slice fixes.
			detail = "this restore needs the updated agent (system.fullbackup.extract_uploaded) — update the panel-agent on this server, then retry"
		}
		writeFullBackupOutcome(marker, fullBackupOutcome{Status: "failed", Error: detail})
		return
	}
	var ex struct {
		Stage string `json:"stage"`
		Users []struct {
			Username  string `json:"username"`
			InnerPath string `json:"inner_path"`
		} `json:"users"`
	}
	if json.Unmarshal(raw, &ex) != nil || ex.Stage == "" {
		writeFullBackupOutcome(marker, fullBackupOutcome{Status: "failed", Error: "container extract returned no staging dir"})
		return
	}
	// Drop the container tar now — no need to hold container + inners together.
	_ = os.Remove(containerPath)
	// Always remove the extraction stage when the loop finishes.
	defer func() {
		cctx, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_, _ = h.cfg.Agent.Call(cctx, "system.fullbackup.cleanup_stage", map[string]any{"stage": ex.Stage})
	}()

	want := map[string]bool{}
	for _, u := range req.Usernames {
		want[u] = true
	}

	var packed, skipped []string
	for _, u := range ex.Users {
		// Flush progress-so-far before each account so a poller sees movement even
		// when the users ahead only produced failure/skip lines.
		writeFullBackupOutcome(marker, fullBackupOutcome{Status: "restoring", Packed: packed, Skipped: skipped})
		if len(want) > 0 && !want[u.Username] {
			skipped = append(skipped, u.Username)
			continue
		}
		if !restoreTargetUsernameRE.MatchString(u.Username) {
			packed = append(packed, u.Username+": invalid username — skipped")
			continue
		}
		target, terr := h.cfg.Users.FindByUsername(ctx, u.Username)
		userCreated := false
		if terr != nil || target == nil {
			if !req.CreateMissing {
				packed = append(packed, u.Username+": user does not exist — enable 'create missing users' or create it first")
				continue
			}
			// create-from-manifest for this user, from its OWN inner tar (a
			// mismatched inner manifest fails the username guard before creating).
			nt, cberr := h.resolveOrCreateUserFromTar(ctx, u.InnerPath, u.Username, req.PackageID)
			if cberr != nil {
				packed = append(packed, u.Username+": "+cberr.Error())
				continue
			}
			target, userCreated = nt, true
		}
		rraw, rerr := h.cfg.Agent.Call(ctx, "backup.restore_from_tar", map[string]any{
			"job_id":          ids.NewULID(),
			"tar_path":        u.InnerPath,
			"target_username": u.Username,
		})
		if rerr != nil {
			line := u.Username + ": " + restoreFailureDetail(rerr)
			if userCreated {
				line += " (account created; a retry restores into it)"
			}
			packed = append(packed, line)
			continue
		}
		var rr struct {
			Applied  []string        `json:"applied"`
			Metadata json.RawMessage `json:"metadata"`
		}
		_ = json.Unmarshal(rraw, &rr)
		metaErrs := h.applyRestoreMetadataForUser(ctx, rr.Metadata, target.ID)
		line := u.Username + ": restored " + strconv.Itoa(len(rr.Applied)) + " item(s)"
		if userCreated {
			line += " (account created — send a recovery link: jabali user password " + u.Username + " --link)"
		}
		if len(metaErrs) > 0 {
			line += "; metadata: " + strings.Join(metaErrs, "; ")
		}
		packed = append(packed, line)
	}

	note := ""
	if req.IncludeSystem {
		note = "system restore is not applied from the panel — run the system_restore CLI on the box"
	}
	writeFullBackupOutcome(marker, fullBackupOutcome{Status: "done", Packed: packed, Skipped: skipped, Error: note})
}

// fullRestoreStatus handles GET /admin/system/full-restore-upload/status?upload_id=…
func (h *backupHandler) fullRestoreStatus(c *gin.Context) {
	adminID, ok := h.restoreUploadAdminID(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	if !uploadIDRE.MatchString(uploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	b, err := os.ReadFile(fullRestoreMarkerPath(adminID, uploadID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.Data(http.StatusOK, "application/json", b)
}
