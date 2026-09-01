package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
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
