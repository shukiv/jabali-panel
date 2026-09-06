// Per-account backup download prep (GH #1408, lxsdevcode).
//
// A per-account download runs a restic materialize (minutes) before the first
// byte streams, so the Download button looked dead the whole time. This adds a
// background prepare-job — mirroring the full-server package flow:
//
//	POST .../download/prepare        → 202, detached materialize, writes a marker
//	GET  .../download/prepare-status → poll { status: preparing|ready|failed }
//	GET  .../download                → streamBackupArtifact reuses the warmed dir
//
// streamBackupArtifact consumes the ready marker itself (consumePreparedDir), so
// the download skips the materialize instead of re-running it. Both the admin
// (/admin/backups/:job_id/…) and tenant (/me/backups/:id/…) surfaces get it.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// downloadPrepMarkerDir is a package var (defaults to the panel-writable upload
// staging dir) only so tests can redirect the marker to a tempdir — production
// always uses restoreUploadDir.
var downloadPrepMarkerDir = restoreUploadDir

func downloadPrepMarkerPath(jobID string) string {
	return filepath.Join(downloadPrepMarkerDir, "dlprep-"+jobID+".json")
}

type downloadPrepOutcome struct {
	Status string `json:"status"` // preparing | ready | failed
	TS     int64  `json:"ts"`
	Path   string `json:"path,omitempty"`
	Error  string `json:"error,omitempty"`
}

func writeDownloadPrepOutcome(path string, o downloadPrepOutcome) {
	o.TS = time.Now().Unix()
	if len(o.Error) > 600 {
		o.Error = o.Error[:600] + "…"
	}
	b, _ := json.Marshal(o)
	writeOutcomeFile(path, b) // atomic swap, shared with #1323/#1408
}

// consumePreparedDir returns the materialized dir a prepare-job warmed for this
// job (or "" if none / not ready / gone), and removes the marker so a later
// download re-materializes fresh rather than trusting a stale path. Called by
// streamBackupArtifact.
func consumePreparedDir(jobID string) string {
	marker := downloadPrepMarkerPath(jobID)
	b, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	var o downloadPrepOutcome
	if json.Unmarshal(b, &o) != nil || o.Status != "ready" || o.Path == "" {
		return ""
	}
	fi, serr := os.Stat(o.Path)
	if serr != nil || !fi.IsDir() {
		_ = os.Remove(marker) // dir already reaped — drop the stale marker
		return ""
	}
	_ = os.Remove(marker)
	return o.Path
}

// downloadJobPreparable reports whether a job can be downloaded/prepared (same
// gate as streamBackupArtifact: a real snapshot from a succeeded/partial run).
func downloadJobPreparable(job *models.BackupJob) (int, string) {
	if job.Status != models.BackupJobStatusSucceeded && job.Status != models.BackupJobStatusPartial {
		return http.StatusNotFound, "no_completed_snapshot"
	}
	if job.SnapshotID == "" {
		return http.StatusUnprocessableEntity, "no_snapshot_id"
	}
	return 0, ""
}

// startDownloadPrepare kicks the detached materialize + records the outcome in
// the job's marker. Idempotent-ish: a second prepare just re-materializes.
func startDownloadPrepare(ag agent.AgentInterface, dests repository.BackupDestinationRepository, ssoKey *ssokey.Key, job *models.BackupJob) {
	marker := downloadPrepMarkerPath(job.ID)
	writeDownloadPrepOutcome(marker, downloadPrepOutcome{Status: "preparing"})
	jobID, snapID := job.ID, job.SnapshotID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		dest, destParams := materializeDest(ctx, dests, job)
		_ = backupwrapperhelpers.WithOptionalDestPassword(ctx, dest, ag, ssoKey, func(passwordFile string) error {
			params := map[string]any{"job_id": jobID, "snapshot_id": snapID}
			for k, v := range destParams {
				params[k] = v
			}
			if passwordFile != "" {
				params["password_file"] = passwordFile
			}
			raw, err := ag.Call(ctx, "backup.materialize", params)
			if err != nil {
				writeDownloadPrepOutcome(marker, downloadPrepOutcome{Status: "failed", Error: firstLineString(err.Error())})
				return nil
			}
			var mat struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(raw, &mat) != nil || mat.Path == "" {
				writeDownloadPrepOutcome(marker, downloadPrepOutcome{Status: "failed", Error: "agent reply parse"})
				return nil
			}
			writeDownloadPrepOutcome(marker, downloadPrepOutcome{Status: "ready", Path: mat.Path})
			return nil
		})
	}()
}

func serveDownloadPrepStatus(c *gin.Context, jobID string) {
	b, err := os.ReadFile(downloadPrepMarkerPath(jobID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "not_found"})
		return
	}
	c.Data(http.StatusOK, "application/json", b)
}

// --- admin surface ---

func (h *backupHandler) downloadPrepare(c *gin.Context) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("job_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if code, msg := downloadJobPreparable(job); code != 0 {
		c.JSON(code, gin.H{"error": msg})
		return
	}
	startDownloadPrepare(h.cfg.Agent, h.cfg.Destinations, h.cfg.SSOKey, job)
	c.JSON(http.StatusAccepted, gin.H{"status": "preparing", "job_id": job.ID})
}

func (h *backupHandler) downloadPrepareStatus(c *gin.Context) {
	serveDownloadPrepStatus(c, c.Param("job_id"))
}

// --- tenant surface (owner-scoped) ---

func (h *meBackupHandler) downloadPrepare(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("id"))
	if err != nil || job.UserID != claims.UserID { // cross-user → 404
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if code, msg := downloadJobPreparable(job); code != 0 {
		c.JSON(code, gin.H{"error": msg})
		return
	}
	startDownloadPrepare(h.cfg.Agent, h.cfg.Destinations, h.cfg.SSOKey, job)
	c.JSON(http.StatusAccepted, gin.H{"status": "preparing", "job_id": job.ID})
}

func (h *meBackupHandler) downloadPrepareStatus(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("id"))
	if err != nil || job.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"status": "not_found"})
		return
	}
	serveDownloadPrepStatus(c, job.ID)
}
