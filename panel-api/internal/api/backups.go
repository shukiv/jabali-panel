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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
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

// fanOutFullServerAccounts enqueues a queued account_backup job for every
// non-admin user, sharing the given run_id + destination, so the backup
// dispatcher backs up each account as part of a Full Server run (GH #502).
// Best-effort: a per-user create failure is logged and skipped, never aborting
// the run. Content is "full" (home + databases + mailboxes) per account.
func (h *backupHandler) fanOutFullServerAccounts(ctx context.Context, destID, runID string) {
	if h.cfg.Users == nil || h.cfg.Jobs == nil {
		return
	}
	notAdmin := false
	users, _, err := h.cfg.Users.List(ctx, repository.ListOptions{Limit: 10000, IsAdmin: &notAdmin})
	if err != nil {
		h.cfg.logErr("full-server fan-out: list users", err)
		return
	}
	for i := range users {
		u := &users[i]
		dID, rID := destID, runID
		acct := &models.BackupJob{
			ID:            ids.NewULID(),
			UserID:        u.ID,
			DestinationID: &dID,
			RunID:         &rID,
			Kind:          models.BackupJobKindAccountBackup,
			Content:       models.BackupContentFull,
			CreatedAt:     time.Now().UTC(),
			Status:        models.BackupJobStatusQueued,
		}
		if err := h.cfg.Jobs.Create(ctx, acct); err != nil {
			h.cfg.logErr("full-server fan-out: create account job", err)
		}
	}
}

func (h *backupHandler) systemCreate(c *gin.Context) {
	var req systemBackupRequest
	_ = c.ShouldBindJSON(&req)
	dest, derr := h.resolveDest(c, req.DestinationID)
	if derr != nil {
		return
	}
	destID := dest.ID
	runID := ids.NewULID()
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        "system",
		DestinationID: &destID,
		RunID:         &runID,
		Kind:          models.BackupJobKindSystemBackup,
		CreatedAt:     time.Now().UTC(),
		Status:        models.BackupJobStatusQueued,
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), job); err != nil {
		h.cfg.logErr("create system backup", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}
	// GH #502: a Full Server backup (include_accounts) is the system backup
	// PLUS a per-account backup for every hosting user, all sharing this run_id
	// + destination so the repo holds the account snapshots restoreAccounts
	// walks on a full restore. The agent's system.backup only covers system
	// state; this account fan-out was missing, so "Full Server" previously
	// backed up System only. The queued account jobs run via the dispatcher.
	if req.IncludeAccounts {
		h.fanOutFullServerAccounts(c.Request.Context(), destID, runID)
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
func validBackupContent(c string) bool { return models.ValidBackupContent(c) }

// normalizeBackupContent maps the empty request default to "full" so the
// job row records what the run actually captured (GH #454): the content
// column drives the Type shown in the backup list. Previously the create
// left Content unset -> GORM applied the column default 'full' -> every
// account backup showed as "Account Backup" regardless of files/db choice.
func normalizeBackupContent(c string) string {
	if c == "" {
		return models.BackupContentFull
	}
	return c
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
	return models.ApplyBackupContent(content, dbs, mbs, pgDbs)
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
		Content:       normalizeBackupContent(req.Content),
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
	// A `partial` backup still produced a valid manifest snapshot (one
	// non-critical stage failed but the rest, incl. the manifest, were
	// captured) — the operator must be able to download it (GH #502): the
	// system leg of a Full Server backup is commonly `partial` yet its
	// snapshot is real. The SnapshotID guard below is the true gate; queued/
	// running/failed/cancelled have no snapshot and 404 here.
	if job.Status != models.BackupJobStatusSucceeded && job.Status != models.BackupJobStatusPartial {
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
	// Backup downloads stream multi-GB archives, and the restic materialize can
	// take minutes — both far exceed the server's 30s WriteTimeout, which would
	// sever the connection mid-stream. GH #462: the download stalled at ~1.15 GB,
	// i.e. exactly 30s of throughput. Clear this request's write deadline so the
	// stream runs to completion (nginx already allows an hour via
	// proxy_send/read_timeout 3600s). Best-effort: a writer that doesn't support
	// deadlines just keeps the default.
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})

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
	// Packages resolves the caller's hosting-package backup limits (GH #454).
	// REQUIRED — RegisterMeBackupRoutes panics if nil, and the create gate fails
	// CLOSED (denies) rather than skipping when the entitlement can't be resolved.
	Packages repository.PackageRepository

	// Notify publishes the backup.limit.reached event to the tenant + admins when
	// the retention cap is hit (GH #454). Optional — nil just skips the notice.
	Notify *notifications.Queue

	// Schedules + Settings power the tenant scheduled-backup card (GH #454
	// Step 4): the tenant owns content/destination/on-off within their package
	// limits, the admin owns the cron TIME (server_settings.tenant_backup_cron).
	// Both are OPTIONAL — the /me/backup-schedule routes mount only when both
	// are non-nil, so a deployment without them simply lacks tenant scheduling.
	Schedules repository.BackupScheduleRepository
	Settings  repository.ServerSettingsRepository

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
	if cfg.Jobs == nil || cfg.Users == nil || cfg.Packages == nil {
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
	g.GET("/destinations", h.destinations)
	// GH #454: tenant-editable backup exclusions (~/.backupignore). Owner-scoped
	// via the session user; layered onto the global exclude list at backup time.
	g.GET("/exclusions", h.getExclusions)
	g.PUT("/exclusions", h.putExclusions)
	// Tenant scheduled-backup card (GH #454 Step 4). Mounted only when the
	// schedule repo + server settings (the admin-owned cron time) are wired.
	if cfg.Schedules != nil && cfg.Settings != nil {
		rg.GET("/me/backup-schedule", h.getSchedule)
		rg.PUT("/me/backup-schedule", h.putSchedule)
	}
}

// meDestView is the SAFE tenant-facing projection of a backup destination
// (GH #454): id/name/kind only. It deliberately omits URL, credentials_ref,
// and extra_options (bucket names, SFTP hosts) so a tenant picker never leaks
// the admin's infrastructure details.
type meDestView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// destinations lists the backup destinations the caller may target — those
// whose kind is in the caller's hosting-package allow-list (GH #454). Admins
// see every enabled destination. allow_local flags whether the plain local
// default (no destination row) is permitted, so the UI can offer it. Fails
// closed: a non-admin whose package can't be resolved sees nothing.
func (h *meBackupHandler) destinations(c *gin.Context) {
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
	var pkg *models.HostingPackage
	if !user.IsAdmin {
		if h.cfg.Packages == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "backup_gating_unavailable"})
			return
		}
		if user.PackageID != nil {
			pkg, _ = h.cfg.Packages.FindByID(c.Request.Context(), *user.PackageID)
		}
	}
	allowKind := func(kind string) bool {
		if user.IsAdmin {
			return true
		}
		return pkg != nil && pkg.AllowsBackupKind(kind)
	}
	out := make([]meDestView, 0)
	if h.cfg.Destinations != nil {
		all, lerr := h.cfg.Destinations.ListEnabled(c.Request.Context())
		if lerr == nil {
			for _, d := range all {
				if allowKind(d.Kind) {
					out = append(out, meDestView{ID: d.ID, Name: d.Name, Kind: d.Kind})
				}
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"data":        out,
		"allow_local": allowKind(models.BackupDestinationKindLocal),
	})
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

// backupIgnoreFilename is the per-account, tenant-editable restic exclude list
// at the home root. Mirrors panel-agent commands.BackupIgnoreFile; kept as a
// local literal to avoid importing the agent package (GH #454).
const backupIgnoreFilename = ".backupignore"

// maxBackupExclusionsBytes caps the .backupignore the tenant may save. A pattern
// list is tiny; the agent's home backup ignores the file above 256 KiB anyway.
const maxBackupExclusionsBytes = 64 * 1024

// backupExclusionsFileNotFound reports whether an agent files.read error is just
// "the file isn't there yet" (the common case for an account that never set
// exclusions) — files.read collapses ENOENT into a generic CodeInternal
// "failed to open file: … no such file or directory", so match on the message.
func backupExclusionsFileNotFound(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such file") ||
		strings.Contains(m, "does not exist") ||
		strings.Contains(m, "enoent")
}

// getExclusions returns the caller's own ~/.backupignore contents (empty when
// the file doesn't exist yet). Owner-scoped: the username is resolved from the
// session user, never the request, so a tenant can only ever read their own file.
func (h *meBackupHandler) getExclusions(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "user_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	path := filepath.Join("/home", *user.Username, backupIgnoreFilename)
	ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "files.read", map[string]any{
		"user_id":  claims.UserID,
		"username": *user.Username,
		"path":     path,
	})
	if err != nil {
		if backupExclusionsFileNotFound(err) {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "patterns": ""})
			return
		}
		status, body := translateAgentError(err)
		c.JSON(status, body)
		return
	}
	var r struct {
		Content  string `json:"content"`
		IsBinary bool   `json:"is_binary"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.IsBinary {
		// A binary .backupignore is nonsense; surface empty so the editor can
		// replace it rather than rendering garbage.
		c.JSON(http.StatusOK, gin.H{"status": "ok", "patterns": ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "patterns": r.Content})
}

// putExclusions overwrites the caller's own ~/.backupignore with the supplied
// restic exclude patterns (owner-scoped, session-derived username). An empty
// body clears the list (writes an empty file). The home backup layers this on
// top of the global exclude list (GH #454).
func (h *meBackupHandler) putExclusions(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "user_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "agent_unavailable"})
		return
	}
	var body struct {
		Patterns string `json:"patterns"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_body"})
		return
	}
	if len(body.Patterns) > maxBackupExclusionsBytes {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "too_large", "detail": "exclude list exceeds 64 KiB"})
		return
	}
	path := filepath.Join("/home", *user.Username, backupIgnoreFilename)
	ctx, cancel := context.WithTimeout(c.Request.Context(), backupCallTimeout)
	defer cancel()
	if _, err := h.cfg.Agent.Call(ctx, "files.write", map[string]any{
		"user_id":  claims.UserID,
		"username": *user.Username,
		"path":     path,
		"content":  body.Patterns,
		"mode":     "overwrite",
	}); err != nil {
		status, respBody := translateAgentError(err)
		c.JSON(status, respBody)
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
	// GH #454: gate tenant self-backups on the hosting-package limit. Admins are
	// exempt (unlimited, they use the admin backup surface). A non-admin tenant
	// needs a package with max_backups > 0, and must be under that retention cap.
	// This gate FAILS CLOSED: any inability to resolve the limit (missing package
	// repo, package load error, no package) denies the backup — a limit gate must
	// never silently allow an ungated tenant backup on a misconfiguration.
	var pkg *models.HostingPackage
	if !user.IsAdmin {
		if h.cfg.Packages == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "backup_gating_unavailable", "detail": "backup entitlement cannot be resolved right now"})
			return
		}
		if user.PackageID == nil {
			c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "backups_not_included", "detail": "your hosting plan does not include self-service backups"})
			return
		}
		var perr error
		pkg, perr = h.cfg.Packages.FindByID(c.Request.Context(), *user.PackageID)
		if perr != nil || pkg == nil {
			// Fail closed: cannot confirm the entitlement -> deny.
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "backup_gating_unavailable", "detail": "backup entitlement cannot be resolved right now"})
			return
		}
		if !pkg.BackupsEnabled() {
			c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "backups_not_included", "detail": "your hosting plan does not include self-service backups"})
			return
		}
		// Retention cap. On a count error, fail closed (deny) rather than risk
		// letting a tenant exceed max_backups.
		_, total, terr := h.cfg.Jobs.ListForUser(c.Request.Context(), user.ID, 1, 0)
		if terr != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "backup_gating_unavailable", "detail": "backup count cannot be resolved right now"})
			return
		}
		if total >= int64(pkg.MaxBackups) {
			// Retention policy (GH #454): the admin picks reject (default, safe) or
			// prune. Prune auto-forgets the tenant's OLDEST OWNED backup(s) to make
			// room; if it can't free enough (e.g. only running jobs remain) it falls
			// back to reject. Either way, the tenant + admins are notified.
			if pkg.BackupRetentionPrunes() && h.pruneOldestOwned(c.Request.Context(), user, int(pkg.MaxBackups)) {
				h.notifyBackupLimit(c.Request.Context(), user, "prune", pkg.MaxBackups)
			} else {
				h.notifyBackupLimit(c.Request.Context(), user, "reject", pkg.MaxBackups)
				c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "backup_limit_reached", "detail": fmt.Sprintf("your plan allows %d backups; delete an old one before creating another", pkg.MaxBackups)})
				return
			}
		}
	}
	var req createBackupRequest // content/folders/compression + optional destination_id
	_ = c.ShouldBindJSON(&req)
	if !validBackupContent(req.Content) || !validBackupCompression(req.Compression) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_option", "detail": "content must be full/files/database/folders; compression must be off/auto/max"})
		return
	}
	// GH #454: resolve + gate the backup destination. The package's allowed
	// destination kinds gate which destinations a tenant may target, INCLUDING
	// the implicit "local" default (a tenant whose plan doesn't allow "local"
	// must pick an allowed remote). Admins are unrestricted.
	var dest *models.BackupDestination
	destKind := models.BackupDestinationKindLocal
	if strings.TrimSpace(req.DestinationID) != "" {
		if h.cfg.Destinations == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "destination_lookup_unavailable"})
			return
		}
		d, derr := h.cfg.Destinations.Get(c.Request.Context(), strings.TrimSpace(req.DestinationID))
		if derr != nil || d == nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unknown_destination", "detail": "no such backup destination"})
			return
		}
		if !d.Enabled {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "destination_disabled", "detail": "that backup destination is disabled"})
			return
		}
		dest = d
		destKind = d.Kind
	}
	if !user.IsAdmin && (pkg == nil || !pkg.AllowsBackupKind(destKind)) {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "destination_not_allowed", "detail": fmt.Sprintf("your hosting plan does not allow the %q backup destination", destKind)})
		return
	}
	job := &models.BackupJob{
		ID:        ids.NewULID(),
		UserID:    user.ID,
		Kind:      models.BackupJobKindAccountBackup,
		Content:   normalizeBackupContent(req.Content),
		CreatedAt: time.Now().UTC(),
		Status:    models.BackupJobStatusQueued,
	}
	if dest != nil {
		did := dest.ID
		job.DestinationID = &did
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
		// Route to the chosen destination (GH #454). destWireParams supplies
		// repo_url + credentials_ref + destination_kind (+ sftp); absent = the
		// agent's local repo default, unchanged.
		if dest != nil {
			for k, v := range destWireParams(dest) {
				params[k] = v
			}
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

// meScheduleView is the tenant's own scheduled-backup state plus the
// admin-owned knobs the card needs to render (GH #454 Step 4). CronExpr and the
// firing times are READ-ONLY to the tenant (the admin owns the timing via
// server_settings.tenant_backup_cron); ScheduledBackupsEnabled + MaxBackups
// come from the hosting package and cap what the tenant may set.
type meScheduleView struct {
	Exists                  bool     `json:"exists"`
	Enabled                 bool     `json:"enabled"`
	Content                 string   `json:"content"`
	DestinationID           string   `json:"destination_id,omitempty"`
	KeepDaily               *int     `json:"keep_daily,omitempty"`
	KeepWeekly              *int     `json:"keep_weekly,omitempty"`
	KeepMonthly             *int     `json:"keep_monthly,omitempty"`
	CronExpr                string   `json:"cron_expr"`
	NextFirings             []string `json:"next_firings,omitempty"`
	ScheduledBackupsEnabled bool     `json:"scheduled_backups_enabled"`
	MaxBackups              uint32   `json:"max_backups"`
}

// meScheduleGate resolves the caller + hosting package for the tenant
// scheduled-backup endpoints, failing CLOSED exactly like the on-demand create
// gate: any inability to resolve the package entitlement denies (it writes the
// error and returns ok=false). Admins pass with a nil package — they manage
// schedules through the admin surface, so PUT still denies them (nil package
// fails the scheduled_backups_enabled check), while GET renders a disabled card.
func (h *meBackupHandler) meScheduleGate(c *gin.Context) (*models.User, *models.HostingPackage, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "unauthenticated"})
		return nil, nil, false
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "user_not_found"})
		return nil, nil, false
	}
	if user.IsAdmin {
		return user, nil, true
	}
	if h.cfg.Packages == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "backup_gating_unavailable"})
		return nil, nil, false
	}
	if user.PackageID == nil {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "backups_not_included", "detail": "your hosting plan does not include self-service backups"})
		return nil, nil, false
	}
	pkg, perr := h.cfg.Packages.FindByID(c.Request.Context(), *user.PackageID)
	if perr != nil || pkg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "backup_gating_unavailable"})
		return nil, nil, false
	}
	return user, pkg, true
}

// findMeSchedule returns the caller's OWN account_backup schedule, or nil. A
// tenant-owned schedule is marked by a non-null user_id == the tenant (admin
// schedules always leave user_id NULL and target users via the join table), so
// ListForUser(userID) returns only the tenant's own rows — a tenant can never
// reach an admin-created fan-out schedule through this path.
func (h *meBackupHandler) findMeSchedule(ctx context.Context, userID string) *models.BackupSchedule {
	rows, err := h.cfg.Schedules.ListForUser(ctx, userID)
	if err != nil {
		return nil
	}
	for i := range rows {
		if rows[i].Kind == models.BackupScheduleKindAccount {
			return &rows[i]
		}
	}
	return nil
}

func (h *meBackupHandler) getSchedule(c *gin.Context) {
	user, pkg, ok := h.meScheduleGate(c)
	if !ok {
		return
	}
	settings, serr := h.cfg.Settings.Get(c.Request.Context())
	if serr != nil || settings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "settings_unavailable"})
		return
	}
	view := meScheduleView{
		Content:  models.BackupContentFull,
		CronExpr: settings.TenantBackupCron,
	}
	if pkg != nil {
		view.ScheduledBackupsEnabled = pkg.BackupsEnabled() && pkg.ScheduledBackupsEnabled
		view.MaxBackups = pkg.MaxBackups
	}
	if fires, ferr := internalbackup.PreviewFires(settings.TenantBackupCron, time.Now().UTC(), 3); ferr == nil {
		for _, f := range fires {
			view.NextFirings = append(view.NextFirings, f.Format(time.RFC3339))
		}
	}
	if sched := h.findMeSchedule(c.Request.Context(), user.ID); sched != nil {
		view.Exists = true
		view.Enabled = sched.Enabled
		view.Content = models.NormalizeBackupContent(sched.Content)
		view.KeepDaily, view.KeepWeekly, view.KeepMonthly = sched.KeepDaily, sched.KeepWeekly, sched.KeepMonthly
		if dests, derr := h.cfg.Schedules.GetDestinations(c.Request.Context(), sched.ID); derr == nil && len(dests) > 0 {
			view.DestinationID = dests[0].ID
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": view})
}

// meScheduleRequest is the tenant-editable schedule payload. cron_expr is NOT a
// field — the tenant can never set the timing; it is always forced from the
// admin-owned server_settings.tenant_backup_cron.
type meScheduleRequest struct {
	Enabled       bool   `json:"enabled"`
	Content       string `json:"content"`
	DestinationID string `json:"destination_id"`
	KeepDaily     *int   `json:"keep_daily"`
	KeepWeekly    *int   `json:"keep_weekly"`
	KeepMonthly   *int   `json:"keep_monthly"`
}

func (h *meBackupHandler) putSchedule(c *gin.Context) {
	user, pkg, ok := h.meScheduleGate(c)
	if !ok {
		return
	}
	// Admins and plans without scheduled backups cannot use the tenant card.
	if pkg == nil || !pkg.BackupsEnabled() || !pkg.ScheduledBackupsEnabled {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "scheduled_backups_not_enabled", "detail": "your hosting plan does not allow scheduled backups"})
		return
	}
	var req meScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_body", "detail": err.Error()})
		return
	}
	if !validBackupContent(req.Content) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_option", "detail": "content must be full/files/database/folders"})
		return
	}
	content := models.NormalizeBackupContent(req.Content)
	// A schedule with no destination is inert (the fan-out skips it), so an
	// enabled schedule MUST carry an allowed, enabled destination. When disabled
	// the tenant may still save their content/destination choice for later.
	destID := strings.TrimSpace(req.DestinationID)
	if destID != "" {
		if h.cfg.Destinations == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "destination_lookup_unavailable"})
			return
		}
		d, derr := h.cfg.Destinations.Get(c.Request.Context(), destID)
		if derr != nil || d == nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unknown_destination", "detail": "no such backup destination"})
			return
		}
		if !d.Enabled {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "destination_disabled", "detail": "that backup destination is disabled"})
			return
		}
		if !pkg.AllowsBackupKind(d.Kind) {
			c.JSON(http.StatusForbidden, gin.H{"status": "error", "error": "destination_not_allowed", "detail": fmt.Sprintf("your hosting plan does not allow the %q backup destination", d.Kind)})
			return
		}
	}
	if req.Enabled && destID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "destination_required", "detail": "pick a backup destination to enable scheduled backups"})
		return
	}
	// Retention: no keep_* may exceed the package's max_backups cap.
	maxKeep := int(pkg.MaxBackups)
	clamp := func(v *int) *int {
		if v == nil {
			return nil
		}
		n := *v
		if n < 0 {
			n = 0
		}
		if n > maxKeep {
			n = maxKeep
		}
		return &n
	}
	keepDaily, keepWeekly, keepMonthly := clamp(req.KeepDaily), clamp(req.KeepWeekly), clamp(req.KeepMonthly)
	// cron_expr is ALWAYS the admin-owned time — never anything from the body.
	settings, serr := h.cfg.Settings.Get(c.Request.Context())
	if serr != nil || settings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "settings_unavailable"})
		return
	}
	next, ferr := internalbackup.NextFire(settings.TenantBackupCron, time.Now().UTC())
	if ferr != nil {
		// A misconfigured admin cron shouldn't 500 the tenant; surface it.
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "schedule_time_invalid", "detail": "the scheduled-backup time set by your host is invalid; contact support"})
		return
	}
	uid := user.ID
	sched := h.findMeSchedule(c.Request.Context(), user.ID)
	if sched == nil {
		sched = &models.BackupSchedule{
			ID:     ids.NewULID(),
			Kind:   models.BackupScheduleKindAccount,
			UserID: &uid,
		}
		sched.CronExpr = settings.TenantBackupCron
		sched.Content = content
		sched.Enabled = req.Enabled
		sched.KeepDaily, sched.KeepWeekly, sched.KeepMonthly = keepDaily, keepWeekly, keepMonthly
		sched.NextRunAt = &next
		if err := h.cfg.Schedules.Create(c.Request.Context(), sched); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
			return
		}
	} else {
		sched.UserID = &uid
		sched.CronExpr = settings.TenantBackupCron
		sched.Content = content
		sched.Enabled = req.Enabled
		sched.KeepDaily, sched.KeepWeekly, sched.KeepMonthly = keepDaily, keepWeekly, keepMonthly
		sched.NextRunAt = &next
		if err := h.cfg.Schedules.Update(c.Request.Context(), sched); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_update"})
			return
		}
	}
	// Scope the fan-out to THIS tenant only (empty user list would fan out to
	// every non-admin user at tick time — never for a tenant-owned schedule).
	if err := h.cfg.Schedules.ReplaceUsers(c.Request.Context(), sched.ID, []string{uid}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_link_users"})
		return
	}
	dests := []string{}
	if destID != "" {
		dests = append(dests, destID)
	}
	if err := h.cfg.Schedules.ReplaceDestinations(c.Request.Context(), sched.ID, dests); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_link_destinations"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// pruneOldestOwned auto-forgets the caller's OLDEST owned account_backup(s)
// until they are under maxBackups, for the "prune" retention policy (GH #454).
// Owner-scoped: OldestAccountBackupForUser filters by user_id, so a tenant can
// only ever prune their OWN backups. Returns true once under the cap; false if
// it can't free enough (e.g. only running/restore jobs remain) -> caller falls
// back to reject. The forget is best-effort (an orphaned snapshot is reclaimed
// by the retention sweep); the row delete is what guarantees loop progress.
func (h *meBackupHandler) pruneOldestOwned(ctx context.Context, user *models.User, maxBackups int) bool {
	for i := 0; i < 1000; i++ { // bounded so an odd repo state can't spin forever
		_, total, err := h.cfg.Jobs.ListForUser(ctx, user.ID, 1, 0)
		if err != nil {
			return false
		}
		if total < int64(maxBackups) {
			return true
		}
		oldest, oerr := h.cfg.Jobs.OldestAccountBackupForUser(ctx, user.ID)
		if oerr != nil || oldest == nil {
			return false
		}
		if h.cfg.Agent != nil {
			fctx, cancel := context.WithTimeout(ctx, backupCallTimeout)
			_, _ = h.cfg.Agent.Call(fctx, "backup.forget", map[string]any{
				"job_id": oldest.ID, "user_id": oldest.UserID, "kind": oldest.Kind,
			})
			cancel()
		}
		if derr := h.cfg.Jobs.Delete(ctx, oldest.ID); derr != nil {
			return false
		}
	}
	return false
}

// notifyBackupLimit fires the backup.limit.reached event to the tenant's own
// inbox (UserID set) AND broadcasts it to every admin bell (UserID empty), with
// bodies tailored to the outcome ("reject" blocked / "prune" auto-removed).
// Best-effort — a notify failure never blocks the backup path (GH #454).
func (h *meBackupHandler) notifyBackupLimit(ctx context.Context, user *models.User, outcome string, limit uint32) {
	if h.cfg.Notify == nil {
		return
	}
	who := user.ID
	if user.Username != nil && *user.Username != "" {
		who = *user.Username
	}
	var tenantBody, adminBody string
	if outcome == "prune" {
		tenantBody = fmt.Sprintf("You reached your plan's backup limit (%d). Your oldest backup was automatically removed to make room for the new one.", limit)
		adminBody = fmt.Sprintf("Tenant %s reached their backup limit (%d); the oldest backup was auto-pruned per the package retention policy.", who, limit)
	} else {
		tenantBody = fmt.Sprintf("You reached your plan's backup limit (%d). Delete an old backup before creating another, or ask your host to raise the limit.", limit)
		adminBody = fmt.Sprintf("Tenant %s reached their backup limit (%d); the new backup was blocked (reject policy).", who, limit)
	}
	nctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = h.cfg.Notify.Publish(nctx, notifications.Envelope{
		EventKind: "backup.limit.reached", Severity: "warning",
		Title: "Backup limit reached", Body: tenantBody, UserID: user.ID,
	})
	_, _ = h.cfg.Notify.Publish(nctx, notifications.Envelope{
		EventKind: "backup.limit.reached", Severity: "warning",
		Title: "Tenant backup limit reached", Body: adminBody,
	})
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
