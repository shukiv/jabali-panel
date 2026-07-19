// M30 Step 8 — REST endpoints for account_backup. System routes live
// in system_backups.go (Step 12). User-shell self-backup endpoints
// land in user_backups.go (Step 9).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupmetadata"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// BackupHandlerConfig is the dependency bundle for both /admin and /me
// backup routes. RestoreStaging is the local path the agent restores
// stages into; download materializes from there.
type BackupHandlerConfig struct {
	Agent          agent.AgentInterface
	Jobs           repository.BackupJobRepository
	Destinations   repository.BackupDestinationRepository
	Users          repository.UserRepository
	Databases      repository.DatabaseRepository
	DatabaseUsers  repository.DatabaseUserRepository
	DatabaseGrants repository.DatabaseUserGrantRepository
	Domains        repository.DomainRepository
	Mailboxes      repository.MailboxRepository
	AppInstalls    repository.ApplicationInstallRepository

	// Schema-v2 metadata producers — every nullable repo here is queried
	// when building the per-user metadata bundle so disaster recovery
	// can rebuild full panel state. Each is optional; missing repos
	// log + skip the corresponding section.
	SSLCerts       repository.SSLCertificateRepository
	PHPPools       repository.PHPPoolRepository
	PHPPoolIni     repository.PHPPoolIniOverrideRepository
	Forwarders     repository.EmailForwarderRepository
	Autoresponders repository.EmailAutoresponderRepository
	MailboxShares  repository.MailboxShareRepository
	DNSSECKeys     repository.DNSSECKeyRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	SSHKeys        repository.SSHKeyRepository
	CronJobs       repository.CronJobRepository
	LimitOverrides repository.UserLimitOverrideRepository
	EgressPolicies repository.UserEgressPolicyRepository
	EgressRequests repository.UserEgressRequestRepository

	// M30.2.x — sso key for unsealing per-destination restic
	// passwords before the agent dispatch. Optional; when nil the
	// password helper falls back to the legacy shared file at
	// /etc/jabali-panel/restic-repo.password (back-compat for
	// destinations that haven't been rotated yet).
	SSOKey *ssokey.Key

	Log             *slog.Logger
	StrictRateLimit gin.HandlerFunc
}

const backupCallTimeout = 10 * time.Second

// restoreCallTimeout: account restores run synchronously on the agent
// (no goroutine fork). 10m covers reasonable home-dir + DB + mailbox
// volumes; larger restores should switch to a background-job model.
const restoreCallTimeout = 10 * time.Minute

// RegisterBackupRoutes mounts the admin-scoped backup endpoints under
// /admin/users/:id/backups + /admin/backups/:job_id. The user-shell
// /me/backups routes live in RegisterUserBackupRoutes.
func RegisterBackupRoutes(rg *gin.RouterGroup, cfg BackupHandlerConfig) {
	if cfg.Jobs == nil {
		panic("api.RegisterBackupRoutes: cfg.Jobs is nil")
	}
	if cfg.Users == nil {
		panic("api.RegisterBackupRoutes: cfg.Users is nil")
	}
	h := &backupHandler{cfg: cfg}

	admin := rg.Group("/admin", middleware.RequireAdmin())
	admin.POST("/users/:id/backups", h.createForUser)
	admin.GET("/users/:id/backups", h.listForUser)
	admin.GET("/backups", h.listAll)
	admin.GET("/backups/:job_id", h.get)
	admin.DELETE("/backups/:job_id", h.delete)
	admin.GET("/backups/:job_id/status", h.status)
	admin.GET("/backups/:job_id/download", h.download)
	admin.POST("/backups/:job_id/cancel", h.cancel)
	admin.GET("/backups/:job_id/logs", h.logs)
	admin.POST("/backups/restore", h.restore)
	admin.GET("/backup-runs", h.listRuns)
	admin.GET("/backup-runs/:run_id/jobs", h.listRunJobs)
	admin.POST("/system/backups", h.systemCreate)
	admin.GET("/system/backups", h.systemList)
	admin.POST("/system/backups/:job_id/cancel", h.systemCancel)
	admin.GET("/system/backups/:job_id/logs", h.logs)
}

func (h *backupHandler) listRuns(c *gin.Context) {
	limit, offset := paginationFromQuery(c, 25, 100)
	runs, total, err := h.cfg.Jobs.ListRuns(c.Request.Context(), limit, offset)
	if err != nil {
		h.cfg.logErr("list backup runs", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	manualLimit, manualOffset := paginationFromQuery(c, 25, 100)
	manual, manualTotal, err := h.cfg.Jobs.ListManual(c.Request.Context(), manualLimit, manualOffset)
	if err != nil {
		h.cfg.logErr("list manual backups", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list_manual"})
		return
	}
	page := offset/maxInt(limit, 1) + 1
	c.JSON(http.StatusOK, gin.H{
		"data":         runs,
		"manual":       manual,
		"manual_total": manualTotal,
		"total":        total,
		"page":         page,
		"page_size":    limit,
	})
}

func (h *backupHandler) listRunJobs(c *gin.Context) {
	runID := c.Param("run_id")
	jobs, err := h.cfg.Jobs.ListByRun(c.Request.Context(), runID)
	if err != nil {
		h.cfg.logErr("list run jobs", err, "run_id", runID)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": jobs})
}

type systemBackupRequest struct {
	IncludeAccounts bool   `json:"include_accounts"`
	DestinationID   string `json:"destination_id,omitempty"`
}

func (h *backupHandler) systemCreate(c *gin.Context) {
	var req systemBackupRequest
	_ = c.ShouldBindJSON(&req)
	dest, derr := h.resolveDest(c, req.DestinationID)
	if derr != nil {
		return
	}
	destID := dest.ID
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        "system",
		DestinationID: &destID,
		Kind:          models.BackupJobKindSystemBackup,
		CreatedAt:     time.Now().UTC(),
		Status:        models.BackupJobStatusQueued,
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), job); err != nil {
		h.cfg.logErr("create system backup", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}
	if h.cfg.Agent != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
		defer cancel()
		params := map[string]any{
			"job_id":           job.ID,
			"include_accounts": req.IncludeAccounts,
		}
		for k, v := range destWireParams(dest) {
			params[k] = v
		}
		if _, err := h.cfg.Agent.Call(ctx, "system.backup", params); err != nil {
			_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), job.ID, models.BackupJobStatusFailed,
				"", "", 0, 0, nil, nil, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": "agent_call_failed"})
			return
		}
		_ = h.cfg.Jobs.MarkStarted(c.Request.Context(), job.ID)
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "job_id": job.ID})
}

func (h *backupHandler) systemList(c *gin.Context) {
	limit, offset := paginationFromQuery(c, 25, 100)
	rows, total, err := h.cfg.Jobs.ListAll(c.Request.Context(), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	// Filter to system_backup kind only.
	out := make([]models.BackupJob, 0, len(rows))
	for _, r := range rows {
		if r.Kind == models.BackupJobKindSystemBackup {
			out = append(out, r)
		}
	}
	page := offset/maxInt(limit, 1) + 1
	c.JSON(http.StatusOK, gin.H{
		"data": out, "total": total, "page": page, "page_size": limit,
	})
}

func (h *backupHandler) systemCancel(c *gin.Context) {
	jobID := c.Param("job_id")
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
	defer cancel()
	if _, err := h.cfg.Agent.Call(ctx, "system.backup_cancel", map[string]string{"job_id": jobID}); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": "agent_call_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type backupHandler struct{ cfg BackupHandlerConfig }

// resolveDest looks up the destination row (or auto-picks the only
// enabled one when destID is empty + exactly one exists), writes a
// 4xx + nil on miss, returns the row + nil on success.
func (h *backupHandler) resolveDest(c *gin.Context, destID string) (*models.BackupDestination, error) {
	if h.cfg.Destinations == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "destinations_repo_unavailable"})
		return nil, errors.New("destinations repo unavailable")
	}
	if destID == "" {
		// No destination supplied — fall back to the single enabled
		// destination if exactly one exists, else 400.
		all, err := h.cfg.Destinations.ListEnabled(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list_destinations"})
			return nil, err
		}
		if len(all) != 1 {
			c.JSON(http.StatusBadRequest, gin.H{
				"status": "error",
				"error":  "destination_id_required",
				"detail": "destination_id required when more than one destination exists",
			})
			return nil, errors.New("destination_id required")
		}
		return &all[0], nil
	}
	d, err := h.cfg.Destinations.Get(c.Request.Context(), destID)
	if err != nil || d == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "destination_not_found"})
		return nil, errors.New("destination not found")
	}
	if !d.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "destination_disabled"})
		return nil, errors.New("destination disabled")
	}
	return d, nil
}

// materializeDestParams resolves the restic repo params for a backup job's
// download from the job's recorded destination. Returns nil when the job has
// no destination (a truly-local backup) so the agent keeps its local default.
// Without this the download always opened the local repo and 404'd remote
// backups with "no snapshots for job" (GH #462).
func materializeDestParams(ctx context.Context, dests repository.BackupDestinationRepository, job *models.BackupJob) map[string]any {
	if dests == nil || job.DestinationID == nil || *job.DestinationID == "" {
		return nil
	}
	d, err := dests.Get(ctx, *job.DestinationID)
	if err != nil || d == nil {
		return nil
	}
	return destWireParams(d)
}

// destWireParams projects a destination into the JSON keys the agent
// backup commands accept. Mirror of backupscheduler.destWireParams;
// kept duplicated to avoid the api → backupscheduler import cycle.
func destWireParams(d *models.BackupDestination) map[string]any {
	out := map[string]any{
		"repo_url":         d.URL,
		"destination_kind": d.Kind,
		"extra_options":    backupwrapperhelpers.ResticOptionsFor(d),
	}
	if d.CredentialsRef != nil {
		out["credentials_ref"] = *d.CredentialsRef
	}
	if d.Kind == models.BackupDestinationKindSFTP {
		if s := d.ExtraOptionsTyped().SFTP; s != nil {
			out["sftp"] = map[string]any{
				"host":     s.Host,
				"user":     s.User,
				"port":     s.Port,
				"path":     s.Path,
				"auth":     s.Auth,
				"key_path": s.KeyPath,
			}
		}
	}
	return out
}

type createBackupRequest struct {
	DestinationID string   `json:"destination_id,omitempty"`
	Databases     []string `json:"databases,omitempty"`
	Mailboxes     []string `json:"mailboxes,omitempty"`
	// GH #294 options. Content: ""/"full" | "files" | "database" | "folders".
	// Folders: home subpaths (content=folders). Compression: ""|"off"|"auto"|"max".
	Content     string   `json:"content,omitempty"`
	Folders     []string `json:"folders,omitempty"`
	Compression string   `json:"compression,omitempty"`
}

// validBackupContent / validBackupCompression whitelist the GH #294 options so a
// bad value can't reach the agent (restic has only off/auto/max — never gzip/xz).
func validBackupContent(c string) bool {
	switch c {
	case "", "full", "files", "database", "folders":
		return true
	}
	return false
}

func validBackupCompression(c string) bool {
	switch c {
	case "", "off", "auto", "max":
		return true
	}
	return false
}

// applyBackupContent zeroes the db/mail selections a content mode excludes:
// "files"/"folders" -> home only; "database" -> dbs only; else (full) unchanged.
func applyBackupContent(content string, dbs, mbs, pgDbs []string) ([]string, []string, []string) {
	switch content {
	case "files", "folders":
		return nil, nil, nil
	case "database":
		return dbs, nil, pgDbs
	default:
		return dbs, mbs, pgDbs
	}
}

func (h *backupHandler) createForUser(c *gin.Context) {
	userID := c.Param("id")
	user, err := h.cfg.Users.FindByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "user_not_found"})
		return
	}
	var req createBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, errEmptyBody) {
		// errEmptyBody isn't a real symbol; tolerate empty bodies too.
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
		return
	}
	if !validBackupContent(req.Content) || !validBackupCompression(req.Compression) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_option", "detail": "content must be full/files/database/folders; compression must be off/auto/max"})
		return
	}
	dest, derr := h.resolveDest(c, req.DestinationID)
	if derr != nil {
		return
	}
	destID := dest.ID
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        user.ID,
		DestinationID: &destID,
		Kind:          models.BackupJobKindAccountBackup,
		SystemdUnit:   "",
		CreatedAt:     time.Now().UTC(),
		Status:        models.BackupJobStatusQueued,
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), job); err != nil {
		h.cfg.logErr("create backup job", err, "user_id", user.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}

	if h.cfg.Agent != nil {
		// Fall back to "every database / mailbox the user owns" when
		// the operator submits empty arrays — that's what the admin
		// "Create backup" button does. Empty arrays leaving DBs and
		// mail untouched would silently produce home-only backups.
		dbs := req.Databases
		if len(dbs) == 0 {
			dbs = h.allUserDatabases(c.Request.Context(), user.ID)
		}
		mbs := req.Mailboxes
		if len(mbs) == 0 {
			mbs = h.allUserMailboxes(c.Request.Context(), user.ID)
		}
		// M37: split mariadb and postgres dbs so the agent dispatches
		// the right dump tool per engine.
		pgDbs := h.cfg.allUserPostgresDatabases(c.Request.Context(), user.ID)
		dbs, mbs, pgDbs = applyBackupContent(req.Content, dbs, mbs, pgDbs)
		params := map[string]any{
			"job_id":             job.ID,
			"user_id":            user.ID,
			"username":           user.Username,
			"email":              user.Email,
			"is_admin":           user.IsAdmin,
			"databases":          dbs,
			"databases_postgres": pgDbs,
			"mailboxes":          mbs,
			"content":            req.Content,
			"folders":            req.Folders,
			"compression":        req.Compression,
			"metadata":           h.cfg.buildAccountMetadata(c.Request.Context(), user),
		}
		for k, v := range destWireParams(dest) {
			params[k] = v
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
		defer cancel()
		// M30.2.x: per-destination password file is provisioned by
		// the agent's write_temp on demand; cleanup runs after
		// dispatch completes (or fails).
		callErr := backupwrapperhelpers.WithDestPasswordFile(ctx, dest, h.cfg.Agent, h.cfg.SSOKey,
			func(passwordFile string) error {
				if passwordFile != "" {
					params["password_file"] = passwordFile
				}
				_, err := h.cfg.Agent.Call(ctx, "backup.create", params)
				return err
			})
		if err := callErr; err != nil {
			// Mark failed so the UI surfaces the issue right away.
			_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), job.ID, models.BackupJobStatusFailed,
				"", "", 0, 0, nil, nil, err.Error())
			respondAgentErrStatus(c, "agent_call_failed", err)
			return
		}
		_ = h.cfg.Jobs.MarkStarted(c.Request.Context(), job.ID)
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":       "ok",
		"job_id":       job.ID,
		"systemd_unit": "jabali-backup-" + job.ID + ".service",
	})
}

func (h *backupHandler) listForUser(c *gin.Context) {
	userID := c.Param("id")
	limit, offset := paginationFromQuery(c, 50, 200)
	rows, total, err := h.cfg.Jobs.ListForUser(c.Request.Context(), userID, limit, offset)
	if err != nil {
		h.cfg.logErr("list backups for user", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	page := offset/maxInt(limit, 1) + 1
	c.JSON(http.StatusOK, gin.H{
		"data": rows, "total": total, "page": page, "page_size": limit,
	})
}

func (h *backupHandler) listAll(c *gin.Context) {
	limit, offset := paginationFromQuery(c, 50, 200)
	ctx := c.Request.Context()
	var rows []models.BackupJob
	var total int64
	var err error
	// Admin owner-scope via ?user_id (#483).
	if uid := c.Query("user_id"); uid != "" {
		if !ids.IsValidULID(uid) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid user_id"})
			return
		}
		rows, total, err = h.cfg.Jobs.ListForUser(ctx, uid, limit, offset)
	} else {
		rows, total, err = h.cfg.Jobs.ListAll(ctx, limit, offset)
	}
	if err != nil {
		h.cfg.logErr("list backups", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	page := offset/maxInt(limit, 1) + 1
	c.JSON(http.StatusOK, gin.H{
		"data": rows, "total": total, "page": page, "page_size": limit,
	})
}

// delete removes a backup run: the agent forgets+prunes the run's restic
// snapshots first, then the DB job row is dropped. forget-then-delete (like the
// docker-app delete) so a failed forget leaves the row for retry rather than
// orphaning snapshots (GH #294).
func (h *backupHandler) delete(c *gin.Context) {
	jobID := c.Param("job_id")
	job, err := h.cfg.Jobs.Get(c.Request.Context(), jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	if h.cfg.Agent != nil {
		if err := h.forgetBackup(c, job); err != nil {
			return // forgetBackup wrote the response; leave the row
		}
	}
	if err := h.cfg.Jobs.Delete(c.Request.Context(), jobID); err != nil {
		h.cfg.logErr("delete backup job", err, "job_id", jobID)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_delete"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// forgetBackup asks the agent to forget+prune the run's snapshots from its
// destination repo. Resolves the job's destination for repo URL + creds; a job
// with no destination (or a since-deleted one) falls back to the default local
// repo. Returns a non-nil error (response already written) on hard failure.
func (h *backupHandler) forgetBackup(c *gin.Context, job *models.BackupJob) error {
	ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
	defer cancel()
	params := map[string]any{"job_id": job.ID, "user_id": job.UserID, "kind": job.Kind}
	var dest *models.BackupDestination
	if job.DestinationID != nil && h.cfg.Destinations != nil {
		if d, derr := h.cfg.Destinations.Get(ctx, *job.DestinationID); derr == nil {
			dest = d
			for k, v := range destWireParams(dest) {
				params[k] = v
			}
		}
	}
	call := func(passwordFile string) error {
		if passwordFile != "" {
			params["password_file"] = passwordFile
		}
		_, err := h.cfg.Agent.Call(ctx, "backup.forget", params)
		return err
	}
	var ferr error
	if dest != nil {
		ferr = backupwrapperhelpers.WithDestPasswordFile(ctx, dest, h.cfg.Agent, h.cfg.SSOKey, call)
	} else {
		ferr = call("")
	}
	if ferr != nil {
		status, body := translateAgentError(ferr)
		c.JSON(status, body)
		return ferr
	}
	return nil
}

func (h *backupHandler) get(c *gin.Context) {
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("job_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": job})
}

func (h *backupHandler) status(c *gin.Context) {
	jobID := c.Param("job_id")
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "backup.status", map[string]string{"job_id": jobID})
	if err != nil {
		respondAgentErrStatus(c, "agent_call_failed", err)
		return
	}
	var resp any
	_ = json.Unmarshal(raw, &resp)
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

func (h *backupHandler) cancel(c *gin.Context) {
	jobID := c.Param("job_id")
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
	defer cancel()
	if _, err := h.cfg.Agent.Call(ctx, "backup.cancel", map[string]string{"job_id": jobID}); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": "agent_call_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// logs proxies journalctl tail of the transient unit
// (jabali-backup-<id>.service or jabali-system-backup-<id>.service)
// via the agent. Same handler covers both kinds — agent picks the
// unit name from the job kind in the DB row.
func (h *backupHandler) logs(c *gin.Context) {
	jobID := c.Param("job_id")
	job, err := h.cfg.Jobs.Get(c.Request.Context(), jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "backup.logs", map[string]any{
		"job_id": job.ID,
		"kind":   job.Kind,
	})
	if err != nil {
		respondAgentErrStatus(c, "agent_call_failed", err)
		return
	}
	var resp any
	_ = json.Unmarshal(raw, &resp)
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

func (h *backupHandler) download(c *gin.Context) {
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("job_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "no_completed_snapshot"})
		return
	}
	streamBackupArtifact(c, h.cfg.Agent, job, materializeDestParams(c.Request.Context(), h.cfg.Destinations, job), h.cfg.logErr)
}

// streamBackupArtifact materializes a SUCCEEDED backup job's restic snapshot
// via the agent and streams it to the client as a tar.zst attachment. Shared
// by the admin (/backups/:job_id/download) and tenant (/me/backups/:id/download
// — GH #266) handlers; the CALLER must load the job and authorize the request
// (admin vs owner) before calling this.
//
// panel-api runs as the jabali user and can read neither
// /etc/jabali-panel/restic-repo.password nor /var/lib/jabali-backups/repo (both
// 0600/0700 root:root), so the restic restore is dispatched to the agent, which
// materializes the snapshot under /var/lib/jabali-backups/downloads/<job_id>/ as
// root:jabali 0750 for the tar to read without elevated privileges.
func streamBackupArtifact(c *gin.Context, ag agent.AgentInterface, job *models.BackupJob, destParams map[string]any, logErr func(string, error, ...any)) {
	if job.Status != models.BackupJobStatusSucceeded {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "no_completed_snapshot"})
		return
	}
	if job.SnapshotID == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "no_snapshot_id"})
		return
	}
	if ag == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	matCtx, matCancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer matCancel()
	// The snapshot lives in the job's DESTINATION repo. Forward repo_url +
	// credentials so the agent opens that repo, not the local default at
	// /var/lib/jabali-backups/repo (which has no snapshots and 404s with
	// "no snapshots for job" — GH #462). destParams is nil for a truly-local
	// backup, in which case the agent keeps its local default.
	params := map[string]any{
		"job_id":      job.ID,
		"snapshot_id": job.SnapshotID,
	}
	for k, v := range destParams {
		params[k] = v
	}
	raw, err := ag.Call(matCtx, "backup.materialize", params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error", "error": "restic_restore_failed", "detail": err.Error(),
		})
		return
	}
	var mat struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &mat); err != nil || mat.Path == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "agent_reply_parse"})
		return
	}
	defer func() {
		// Best-effort cleanup; a stale dir is recovered by the next
		// download (handler RemoveAll's before re-restoring) or by a
		// future cron sweeper.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = ag.Call(cleanCtx, "backup.materialize_cleanup", map[string]string{"job_id": job.ID})
	}()

	// GH #462: choose the compressor by what's actually installed. A box that
	// lacks the zstd binary (installed before zstd became a dependency, then
	// only ever `jabali update`d) falls back to an uncompressed tar so the
	// download always works.
	useZstd := false
	if _, lerr := exec.LookPath("zstd"); lerr == nil {
		useZstd = true
	}
	filename := job.ID + ".tar"
	contentType := "application/x-tar"

	// GH #462 round 3: build the archive as a Go pipeline (tar -> zstd), NOT
	// `tar -I zstd` / `tar --zstd`. Those run the compressor through /bin/sh,
	// which the panel's AppArmor profile deliberately does not grant (no shell),
	// so under enforce the download died with "zstd: Cannot exec". Two direct
	// execs stay on the profile's tar + zstd allowlist and never touch a shell.
	tarCmd := exec.CommandContext(c.Request.Context(), "tar", "-cf", "-",
		"-C", filepath.Dir(mat.Path), filepath.Base(mat.Path))
	var tarErr, zstdErr bytes.Buffer
	tarCmd.Stderr = &tarErr

	streamCmd := tarCmd // the process whose stdout is streamed to the client
	if useZstd {
		filename = job.ID + ".tar.zst"
		contentType = "application/zstd"
		zstdCmd := exec.CommandContext(c.Request.Context(), "zstd", "-c", "-")
		zstdCmd.Stderr = &zstdErr
		tarOut, perr := tarCmd.StdoutPipe()
		if perr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "tar_pipe", "detail": perr.Error()})
			return
		}
		zstdCmd.Stdin = tarOut
		streamCmd = zstdCmd
	}
	stdout, perr := streamCmd.StdoutPipe()
	if perr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "tar_pipe", "detail": perr.Error()})
		return
	}
	// Start tar first so its stdout is flowing before zstd reads it.
	if serr := tarCmd.Start(); serr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "tar_start", "detail": serr.Error()})
		return
	}
	if useZstd {
		if serr := streamCmd.Start(); serr != nil {
			_ = tarCmd.Process.Kill()
			_ = tarCmd.Wait()
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "zstd_start", "detail": serr.Error()})
			return
		}
	}
	// GH #462: read the first chunk BEFORE sending the download headers. If tar
	// produces no output (missing binary, unreadable materialized path,
	// permission denied), return a real JSON error instead of committing the
	// browser to a silent 0-byte download + "Network Failed".
	head := make([]byte, 64*1024)
	n, rerr := io.ReadFull(stdout, head)
	if n == 0 {
		_ = streamCmd.Wait()
		if useZstd {
			_ = tarCmd.Wait()
		}
		detail := strings.TrimSpace(tarErr.String() + " " + zstdErr.String())
		logErr("tar download produced no output", rerr, "job_id", job.ID, "stderr", detail)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error", "error": "archive_empty",
			"detail": "could not build the download archive: " + detail,
		})
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", contentDisposition("attachment", filename))
	if _, werr := c.Writer.Write(head[:n]); werr != nil {
		logErr("tar download write head", werr, "job_id", job.ID)
	}
	if rerr == nil { // more than the first chunk remains
		_, _ = io.Copy(c.Writer, stdout)
	}
	if werr := streamCmd.Wait(); werr != nil {
		logErr("archive download", werr, "job_id", job.ID, "stderr", strings.TrimSpace(tarErr.String()+" "+zstdErr.String()))
	}
	if useZstd {
		if werr := tarCmd.Wait(); werr != nil {
			logErr("archive download (tar)", werr, "job_id", job.ID, "stderr", strings.TrimSpace(tarErr.String()))
		}
	}
}

type restoreRequest struct {
	ManifestSnapshotID string `json:"manifest_snapshot_id"`
	TargetUserID       string `json:"target_user_id"`
	Overwrite          bool   `json:"overwrite"`
	// DestinationID resolves the restic repo URL + credentials for the
	// agent. Without it the agent defaults to the local repo at
	// /var/lib/jabali-backups/repo, which silently 404s on snapshot
	// lookup whenever the original backup landed on a remote dest.
	DestinationID string `json:"destination_id,omitempty"`
}

func (h *backupHandler) restore(c *gin.Context) {
	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
		return
	}
	if req.ManifestSnapshotID == "" || req.TargetUserID == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "manifest_snapshot_id_and_target_user_id_required"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}

	// Resolve destination → repo_url + credentials (and SFTP block when
	// applicable). Empty destination_id falls back to the single-enabled
	// dest auto-pick already implemented for create flows; if the host
	// has many destinations and none was supplied resolveDest 422s.
	dest, derr := h.resolveDest(c, req.DestinationID)
	if derr != nil {
		return
	}

	// Resolve target user → username so the agent's apply step can
	// chown home + scope mariadb loads. The system user must already
	// exist on this host (cross-host restore is out of scope for v1).
	targetUser, uerr := h.cfg.Users.FindByID(c.Request.Context(), req.TargetUserID)
	if uerr != nil || targetUser == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "target_user_not_found"})
		return
	}

	destID := dest.ID
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        req.TargetUserID,
		DestinationID: &destID,
		Kind:          models.BackupJobKindAccountRestore,
		CreatedAt:     time.Now().UTC(),
		Status:        models.BackupJobStatusQueued,
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), job); err != nil {
		h.cfg.logErr("create restore job", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}
	// backup.restore is synchronous on the agent (no goroutine fork like
	// backup.create). Use a long timeout so real-world account restores
	// (db dumps + mailbox imports) don't hard-fail at 10s, but cap at a
	// ceiling that matches operator UX expectations for the "Restore"
	// button — anything longer should run as a tracked background job.
	ctx, cancel := context.WithTimeout(c.Request.Context(), restoreCallTimeout)
	defer cancel()
	_ = h.cfg.Jobs.MarkStarted(c.Request.Context(), job.ID)
	params := map[string]any{
		"job_id":               job.ID,
		"manifest_snapshot_id": req.ManifestSnapshotID,
		"target_user_id":       req.TargetUserID,
		"target_username":      targetUser.Username,
		"overwrite":            req.Overwrite,
	}
	for k, v := range destWireParams(dest) {
		params[k] = v
	}
	var raw json.RawMessage
	err := backupwrapperhelpers.WithDestPasswordFile(ctx, dest, h.cfg.Agent, h.cfg.SSOKey,
		func(passwordFile string) error {
			if passwordFile != "" {
				params["password_file"] = passwordFile
			}
			var callErr error
			raw, callErr = h.cfg.Agent.Call(ctx, "backup.restore", params)
			return callErr
		})
	if err != nil {
		_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), job.ID, models.BackupJobStatusFailed,
			"", "", 0, 0, nil, nil, err.Error())
		respondAgentErrStatus(c, "agent_call_failed", err)
		return
	}
	// Parse the agent's restore result so the job row reflects what
	// actually happened (succeeded / partial / failed based on stages).
	var result struct {
		JobID  string `json:"job_id"`
		Stages []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Error  string `json:"error,omitempty"`
		} `json:"stages"`
	}
	finalStatus := models.BackupJobStatusSucceeded
	finalErr := ""
	if uerr := json.Unmarshal(raw, &result); uerr == nil {
		failed := 0
		for _, s := range result.Stages {
			if s.Status == "failed" {
				failed++
				if finalErr == "" {
					finalErr = fmt.Sprintf("%s: %s", s.Name, s.Error)
				}
			}
		}
		switch {
		case failed > 0 && failed < len(result.Stages):
			finalStatus = models.BackupJobStatusPartial
		case failed > 0:
			finalStatus = models.BackupJobStatusFailed
		}
	}
	_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), job.ID, finalStatus,
		"", "", 0, 0, raw, nil, finalErr)
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "job_id": job.ID})
}

// --- helpers + sentinel below ---

var errEmptyBody = errors.New("empty body")

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (cfg BackupHandlerConfig) logErr(msg string, err error, kv ...any) {
	if cfg.Log == nil {
		return
	}
	args := append([]any{"err", err}, kv...)
	cfg.Log.Warn(msg, args...)
}

func (cfg MeBackupsHandlerConfig) logErr(msg string, err error, kv ...any) {
	if cfg.Log == nil {
		return
	}
	args := append([]any{"err", err}, kv...)
	cfg.Log.Warn(msg, args...)
}

// buildAccountMetadata delegates to the shared producer in
// panel-api/internal/backupmetadata so the admin and user-shell
// handlers stay in lockstep with the scheduler on schema changes.
func (cfg BackupHandlerConfig) buildAccountMetadata(ctx context.Context, user *models.User) *internalbackup.AccountMetadata {
	return backupmetadata.Build(ctx, user, backupmetadata.Deps{
		Databases: cfg.Databases, DatabaseUsers: cfg.DatabaseUsers, DatabaseGrants: cfg.DatabaseGrants,
		Domains: cfg.Domains, Mailboxes: cfg.Mailboxes, AppInstalls: cfg.AppInstalls,
		SSLCerts: cfg.SSLCerts, PHPPools: cfg.PHPPools, PHPPoolIni: cfg.PHPPoolIni,
		Forwarders: cfg.Forwarders, Autoresponders: cfg.Autoresponders, MailboxShares: cfg.MailboxShares,
		DNSSECKeys: cfg.DNSSECKeys, DNSZones: cfg.DNSZones, DNSRecords: cfg.DNSRecords, SSHKeys: cfg.SSHKeys, CronJobs: cfg.CronJobs,
		LimitOverrides: cfg.LimitOverrides, EgressPolicies: cfg.EgressPolicies, EgressRequests: cfg.EgressRequests,
		Log: cfg.Log,
	})
}

// allUserDatabases returns every MariaDB database name owned by a user.
// Used by manual + self-shell backup paths to default to "everything"
// when the operator submits an empty list. Errors are logged + an
// empty slice returned so a transient repo failure doesn't fall
// through into a "the agent backed up nothing" silent failure.
//
// M37: filters to engine='mariadb' so the legacy `databases` param on
// the agent's backup.create call only carries MariaDB names — PG
// names go in the parallel databases_postgres slice.
func (cfg BackupHandlerConfig) allUserDatabases(ctx context.Context, userID string) []string {
	return cfg.allUserDatabasesByEngine(ctx, userID, "mariadb")
}

// allUserPostgresDatabases — M37 sibling. Returns every PostgreSQL
// database name owned by a user. Backup orchestrator passes this
// to backup.create as databases_postgres alongside the mariadb list.
func (cfg BackupHandlerConfig) allUserPostgresDatabases(ctx context.Context, userID string) []string {
	return cfg.allUserDatabasesByEngine(ctx, userID, "postgres")
}

func (cfg BackupHandlerConfig) allUserDatabasesByEngine(ctx context.Context, userID, engine string) []string {
	if cfg.Databases == nil {
		return nil
	}
	rows, _, err := cfg.Databases.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		cfg.logErr("list databases for backup", err, "user_id", userID, "engine", engine)
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Engine == engine || (engine == "mariadb" && r.Engine == "") {
			out = append(out, r.Name)
		}
	}
	return out
}

// allUserMailboxes returns every mailbox EmailCached for a user, by
// joining domains.list_by_user → mailboxes.list_by_domain. Errors
// per-domain are tolerated (warn + skip) so one bad domain doesn't
// hide the rest of the user's mail.
func (cfg BackupHandlerConfig) allUserMailboxes(ctx context.Context, userID string) []string {
	if cfg.Domains == nil || cfg.Mailboxes == nil {
		return nil
	}
	doms, _, err := cfg.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		cfg.logErr("list domains for backup", err, "user_id", userID)
		return nil
	}
	var out []string
	for _, d := range doms {
		mbs, _, err := cfg.Mailboxes.ListByDomainID(ctx, d.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			cfg.logErr("list mailboxes for backup", err, "domain_id", d.ID)
			continue
		}
		for _, m := range mbs {
			out = append(out, m.EmailCached)
		}
	}
	return out
}

// allUserDatabases shadow that the user-shell handler reaches via h.cfg
// since it lives on the same config bundle. Same for mailboxes.
func (h *backupHandler) allUserDatabases(ctx context.Context, userID string) []string {
	return h.cfg.allUserDatabases(ctx, userID)
}

func (h *backupHandler) allUserMailboxes(ctx context.Context, userID string) []string {
	return h.cfg.allUserMailboxes(ctx, userID)
}

// MeBackupsHandlerConfig wires the user-shell endpoints. Auth check
// uses ginctx.Claims to scope the request to the caller's own user_id.
type MeBackupsHandlerConfig struct {
	Agent          agent.AgentInterface
	Jobs           repository.BackupJobRepository
	Destinations   repository.BackupDestinationRepository
	Users          repository.UserRepository
	Databases      repository.DatabaseRepository
	DatabaseUsers  repository.DatabaseUserRepository
	DatabaseGrants repository.DatabaseUserGrantRepository
	Domains        repository.DomainRepository
	Mailboxes      repository.MailboxRepository
	AppInstalls    repository.ApplicationInstallRepository

	SSLCerts       repository.SSLCertificateRepository
	PHPPools       repository.PHPPoolRepository
	PHPPoolIni     repository.PHPPoolIniOverrideRepository
	Forwarders     repository.EmailForwarderRepository
	Autoresponders repository.EmailAutoresponderRepository
	MailboxShares  repository.MailboxShareRepository
	DNSSECKeys     repository.DNSSECKeyRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	SSHKeys        repository.SSHKeyRepository
	CronJobs       repository.CronJobRepository
	LimitOverrides repository.UserLimitOverrideRepository
	EgressPolicies repository.UserEgressPolicyRepository
	EgressRequests repository.UserEgressRequestRepository

	Log *slog.Logger
}

// buildAccountMetadata projects MeBackupsHandlerConfig into the shared
// metadataDeps consumer. Same schema-v2 producer as BackupHandlerConfig.
func (cfg MeBackupsHandlerConfig) buildAccountMetadata(ctx context.Context, user *models.User) *internalbackup.AccountMetadata {
	return backupmetadata.Build(ctx, user, backupmetadata.Deps{
		Databases: cfg.Databases, DatabaseUsers: cfg.DatabaseUsers, DatabaseGrants: cfg.DatabaseGrants,
		Domains: cfg.Domains, Mailboxes: cfg.Mailboxes, AppInstalls: cfg.AppInstalls,
		SSLCerts: cfg.SSLCerts, PHPPools: cfg.PHPPools, PHPPoolIni: cfg.PHPPoolIni,
		Forwarders: cfg.Forwarders, Autoresponders: cfg.Autoresponders, MailboxShares: cfg.MailboxShares,
		DNSSECKeys: cfg.DNSSECKeys, DNSZones: cfg.DNSZones, DNSRecords: cfg.DNSRecords, SSHKeys: cfg.SSHKeys, CronJobs: cfg.CronJobs,
		LimitOverrides: cfg.LimitOverrides, EgressPolicies: cfg.EgressPolicies, EgressRequests: cfg.EgressRequests,
		Log: cfg.Log,
	})
}

func (cfg MeBackupsHandlerConfig) allUserDatabases(ctx context.Context, userID string) []string {
	return cfg.allUserDatabasesByEngine(ctx, userID, "mariadb")
}

func (cfg MeBackupsHandlerConfig) allUserPostgresDatabases(ctx context.Context, userID string) []string {
	return cfg.allUserDatabasesByEngine(ctx, userID, "postgres")
}

func (cfg MeBackupsHandlerConfig) allUserDatabasesByEngine(ctx context.Context, userID, engine string) []string {
	if cfg.Databases == nil {
		return nil
	}
	rows, _, err := cfg.Databases.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Engine == engine || (engine == "mariadb" && r.Engine == "") {
			out = append(out, r.Name)
		}
	}
	return out
}

func (cfg MeBackupsHandlerConfig) allUserMailboxes(ctx context.Context, userID string) []string {
	if cfg.Domains == nil || cfg.Mailboxes == nil {
		return nil
	}
	doms, _, err := cfg.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range doms {
		mbs, _, err := cfg.Mailboxes.ListByDomainID(ctx, d.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			continue
		}
		for _, m := range mbs {
			out = append(out, m.EmailCached)
		}
	}
	return out
}

// RegisterMeBackupRoutes mounts the user-shell self-backup endpoints
// under /me/backups. Route registers off the v1 group; auth comes from
// the Kratos session middleware already on `rg`.
func RegisterMeBackupRoutes(rg *gin.RouterGroup, cfg MeBackupsHandlerConfig) {
	if cfg.Jobs == nil || cfg.Users == nil {
		panic("api.RegisterMeBackupRoutes: nil dep")
	}
	h := &meBackupHandler{cfg: cfg}
	g := rg.Group("/me/backups")
	g.POST("", h.create)
	g.GET("", h.list)
	g.GET("/:id/download", h.download)
	g.DELETE("/:id", h.delete)
	g.GET("/:id/manifest", h.manifest)
	g.POST("/:id/restore", h.restoreSelective)
}

type meBackupHandler struct{ cfg MeBackupsHandlerConfig }

// delete forgets the caller's own backup run (default local repo — me-backups
// never use a destination) then drops the job row (GH #294).
func (h *meBackupHandler) delete(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	jobID := c.Param("id")
	job, err := h.cfg.Jobs.Get(c.Request.Context(), jobID)
	if err != nil || job.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	if h.cfg.Agent != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
		defer cancel()
		if _, err := h.cfg.Agent.Call(ctx, "backup.forget", map[string]any{
			"job_id": job.ID, "user_id": job.UserID, "kind": job.Kind,
		}); err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return // leave the row for retry
		}
	}
	if err := h.cfg.Jobs.Delete(c.Request.Context(), jobID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_delete"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *meBackupHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "user_not_found"})
		return
	}
	var req createBackupRequest // reuse: content/folders/compression (destination ignored for me-backups)
	_ = c.ShouldBindJSON(&req)
	if !validBackupContent(req.Content) || !validBackupCompression(req.Compression) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_option", "detail": "content must be full/files/database/folders; compression must be off/auto/max"})
		return
	}
	job := &models.BackupJob{
		ID:        ids.NewULID(),
		UserID:    user.ID,
		Kind:      models.BackupJobKindAccountBackup,
		CreatedAt: time.Now().UTC(),
		Status:    models.BackupJobStatusQueued,
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), job); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}
	if h.cfg.Agent != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
		defer cancel()
		dbs, mbs, pgDbs := applyBackupContent(req.Content,
			h.cfg.allUserDatabases(c.Request.Context(), user.ID),
			h.cfg.allUserMailboxes(c.Request.Context(), user.ID),
			h.cfg.allUserPostgresDatabases(c.Request.Context(), user.ID))
		params := map[string]any{
			"job_id":             job.ID,
			"user_id":            user.ID,
			"username":           user.Username,
			"email":              user.Email,
			"is_admin":           user.IsAdmin,
			"databases":          dbs,
			"databases_postgres": pgDbs,
			"mailboxes":          mbs,
			"content":            req.Content,
			"folders":            req.Folders,
			"compression":        req.Compression,
			"metadata":           h.cfg.buildAccountMetadata(c.Request.Context(), user),
		}
		if _, err := h.cfg.Agent.Call(ctx, "backup.create", params); err != nil {
			_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), job.ID, models.BackupJobStatusFailed,
				"", "", 0, 0, nil, nil, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": "agent_call_failed"})
			return
		}
		_ = h.cfg.Jobs.MarkStarted(c.Request.Context(), job.ID)
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "job_id": job.ID})
}

func (h *meBackupHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	limit, offset := paginationFromQuery(c, 25, 100)
	rows, total, err := h.cfg.Jobs.ListForUser(c.Request.Context(), claims.UserID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	page := offset/maxInt(limit, 1) + 1
	c.JSON(http.StatusOK, gin.H{
		"data": rows, "total": total, "page": page, "page_size": limit,
	})
}

type meRestoreSelectiveRequest struct {
	Databases  []string `json:"databases"`
	Home       bool     `json:"home"`
	Mailboxes  []string `json:"mailboxes"`
	DNSDomains []string `json:"dns_domains"`
	Overwrite  bool     `json:"overwrite"`
}

// restoreSelective restores a chosen subset of the caller's OWN backup
// (databases only, GH #267 Wave 2). DESTRUCTIVE + fail-closed: nothing is
// written unless overwrite=true, target_username is server-derived (never from
// the body), and each requested DB must be both in the backup AND currently
// owned by the caller. Home/mail/DNS are out of v1 (see the blueprint).
func (h *meBackupHandler) restoreSelective(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("id"))
	if err != nil || job.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	if job.Status != models.BackupJobStatusSucceeded {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "no_completed_snapshot"})
		return
	}
	if job.SnapshotID == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "no_snapshot_id"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}

	var req meRestoreSelectiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "validation_failed", "detail": err.Error()})
		return
	}
	if len(req.Databases) == 0 && !req.Home && len(req.DNSDomains) == 0 && len(req.Mailboxes) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "nothing_selected",
			"detail": "select at least one database, mailbox, home, and/or dns domain"})
		return
	}

	// Server-derive the target username from the caller — NEVER trust a body.
	owner, oerr := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if oerr != nil || owner == nil || owner.Username == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "owner_lookup_failed"})
		return
	}

	// Every requested DB must be CURRENTLY owned by the caller — fail-closed.
	// (The agent additionally restores only DBs present in the manifest.)
	owned := map[string]bool{}
	if h.cfg.Databases != nil {
		dbs, _, derr := h.cfg.Databases.ListByUserID(c.Request.Context(), claims.UserID, repository.ListOptions{Limit: 10000})
		if derr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list_failed"})
			return
		}
		for _, d := range dbs {
			owned[d.Name] = true
		}
	}
	for _, name := range req.Databases {
		if !owned[name] {
			c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "database_not_owned",
				"detail": "you do not own a database named " + name})
			return
		}
	}

	// DNS domains must also be currently owned by the caller (name -> id map).
	ownedDomains := map[string]string{}
	if (len(req.DNSDomains) > 0 || len(req.Mailboxes) > 0) && h.cfg.Domains != nil {
		doms, _, derr := h.cfg.Domains.ListByUserID(c.Request.Context(), claims.UserID, repository.ListOptions{Limit: 10000})
		if derr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "domain_list_failed"})
			return
		}
		for _, d := range doms {
			ownedDomains[d.Name] = d.ID
		}
		for _, n := range req.DNSDomains {
			if _, ok := ownedDomains[n]; !ok {
				c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "domain_not_owned",
					"detail": "you do not own a domain named " + n})
				return
			}
		}
		for _, mb := range req.Mailboxes {
			at := strings.LastIndex(mb, "@")
			if at <= 0 {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "invalid_mailbox", "detail": mb})
				return
			}
			if _, ok := ownedDomains[mb[at+1:]]; !ok {
				c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "mailbox_not_owned",
					"detail": "you do not own the domain for mailbox " + mb})
				return
			}
		}
	}

	// Track the restore as its own job so the jobs list + notifications cover it.
	restoreJob := &models.BackupJob{
		ID:        ids.NewULID(),
		UserID:    claims.UserID,
		Kind:      models.BackupJobKindAccountRestore,
		CreatedAt: time.Now().UTC(),
		Status:    models.BackupJobStatusQueued,
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), restoreJob); err != nil {
		h.cfg.logErr("create selective restore job", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}
	_ = h.cfg.Jobs.MarkStarted(c.Request.Context(), restoreJob.ID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), restoreCallTimeout)
	defer cancel()

	applied := []string{}
	skipped := []string{}
	warnings := []string{}

	// Databases + home go to the agent (host-side). Only dispatch when one is
	// requested — the agent rejects an empty db+home set.
	if len(req.Databases) > 0 || req.Home || len(req.Mailboxes) > 0 {
		raw, aerr := h.cfg.Agent.Call(ctx, "backup.restore_selective", map[string]any{
			"job_id":               restoreJob.ID,
			"manifest_snapshot_id": job.SnapshotID,
			"target_username":      *owner.Username,
			"databases":            req.Databases,
			"mailboxes":            req.Mailboxes,
			"home":                 req.Home,
			"overwrite":            req.Overwrite,
		})
		if aerr != nil {
			_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), restoreJob.ID, models.BackupJobStatusFailed,
				"", "", 0, 0, nil, nil, aerr.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "restore_failed", "detail": aerr.Error()})
			return
		}
		var ar struct {
			Applied  []string `json:"applied"`
			Skipped  []string `json:"skipped"`
			Warnings []string `json:"warnings"`
		}
		_ = json.Unmarshal(raw, &ar)
		applied = append(applied, ar.Applied...)
		skipped = append(skipped, ar.Skipped...)
		warnings = append(warnings, ar.Warnings...)
	}

	// DNS records are panel-side DB rows — restore them here (owner-scoped),
	// reading the captured records out of the backup metadata via the agent.
	if len(req.DNSDomains) > 0 {
		if !req.Overwrite {
			for _, n := range req.DNSDomains {
				skipped = append(skipped, "dns:"+n)
			}
			warnings = append(warnings, "overwrite=false: DNS not applied. Restoring DNS replaces this domain's custom records; re-send with overwrite=true.")
		} else {
			da, dw := h.restoreDNSRecords(ctx, job.SnapshotID, req.DNSDomains, ownedDomains)
			applied = append(applied, da...)
			warnings = append(warnings, dw...)
		}
	}

	_ = h.cfg.Jobs.MarkFinished(c.Request.Context(), restoreJob.ID, models.BackupJobStatusSucceeded,
		"", "", 0, 0, nil, nil, "")
	c.JSON(http.StatusOK, gin.H{"job_id": restoreJob.ID, "applied": applied, "skipped": skipped, "warnings": warnings})
}

// restoreDNSRecords reads the captured user DNS records from the backup metadata
// (via the agent) and re-inserts them into each domain's zone, replacing the
// existing NON-managed records. Owner-scoping is enforced by ownedDomains (the
// caller's name->id map); managed records are left untouched (they re-derive).
func (h *meBackupHandler) restoreDNSRecords(ctx context.Context, manifestSnap string, domains []string, ownedDomains map[string]string) ([]string, []string) {
	var applied, warnings []string
	if h.cfg.DNSZones == nil || h.cfg.DNSRecords == nil {
		return nil, []string{"dns: not configured on this panel"}
	}
	raw, err := h.cfg.Agent.Call(ctx, "backup.dns_read", map[string]any{
		"manifest_snapshot_id": manifestSnap,
		"domain_names":         domains,
	})
	if err != nil {
		return nil, []string{"dns: read from backup failed: " + err.Error()}
	}
	var rd struct {
		Domains []struct {
			Name       string `json:"name"`
			DNSRecords []struct {
				Name      string `json:"name"`
				Type      string `json:"type"`
				Content   string `json:"content"`
				TTL       int    `json:"ttl"`
				Priority  int    `json:"priority"`
				IsEnabled bool   `json:"is_enabled"`
			} `json:"dns_records"`
		} `json:"domains"`
	}
	if uerr := json.Unmarshal(raw, &rd); uerr != nil {
		return nil, []string{"dns: parse backup metadata failed"}
	}
	have := map[string]bool{}
	for _, d := range rd.Domains {
		have[d.Name] = true
		domID, ok := ownedDomains[d.Name]
		if !ok {
			continue // not owned (already validated, defensive)
		}
		zone, zerr := h.cfg.DNSZones.FindByDomainID(ctx, domID)
		if zerr != nil || zone == nil {
			warnings = append(warnings, "dns "+d.Name+": no DNS zone — skipped")
			continue
		}
		// Replace the zone's existing user records.
		if existing, lerr := h.cfg.DNSRecords.ListByZoneID(ctx, zone.ID); lerr == nil {
			for _, e := range existing {
				if !e.Managed {
					_ = h.cfg.DNSRecords.Delete(ctx, e.ID)
				}
			}
		}
		n := 0
		for _, rec := range d.DNSRecords {
			if cerr := h.cfg.DNSRecords.Create(ctx, &models.DNSRecord{
				ID: ids.NewULID(), ZoneID: zone.ID,
				Name: rec.Name, Type: rec.Type, Content: rec.Content,
				TTL: rec.TTL, Priority: rec.Priority,
				Managed: false, IsEnabled: rec.IsEnabled,
			}); cerr == nil {
				n++
			}
		}
		applied = append(applied, fmt.Sprintf("dns → %s (%d records; reconciler re-publishes)", d.Name, n))
	}
	for _, n := range domains {
		if !have[n] {
			warnings = append(warnings, "dns "+n+": no custom records captured in this backup (older backup, or none) — skipped")
		}
	}
	return applied, warnings
}

// manifest returns the backup's stage/item preview (GH #267 Wave 1) so the
// tenant restore UI can show what's inside one of the caller's own backups
// WITHOUT materializing it. Read-only; owner-scoped (cross-user → 404).
func (h *meBackupHandler) manifest(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("id"))
	if err != nil || job.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	if job.Status != models.BackupJobStatusSucceeded {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "no_completed_snapshot"})
		return
	}
	if job.SnapshotID == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "no_snapshot_id"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "backup.manifest_read", map[string]any{
		"manifest_snapshot_id": job.SnapshotID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error", "error": "manifest_read_failed", "detail": err.Error(),
		})
		return
	}
	// Pass the agent's {kind,user_id,username,stages[]} through verbatim.
	c.Data(http.StatusOK, "application/json", raw)
}

func (h *meBackupHandler) download(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	job, err := h.cfg.Jobs.Get(c.Request.Context(), c.Param("id"))
	// Cross-user attempt → 404 not 403, matches plan Step 9 spec.
	if err != nil || job.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
		return
	}
	// GH #266: actually materialize + stream the tar.zst (the v1 stub
	// returned job metadata as JSON, so the browser's <a href> download
	// got a JSON blob, never a file). Same agent-materialize path as the
	// admin handler, scoped to the caller's own job by the check above.
	streamBackupArtifact(c, h.cfg.Agent, job, materializeDestParams(c.Request.Context(), h.cfg.Destinations, job), h.cfg.logErr)
}
