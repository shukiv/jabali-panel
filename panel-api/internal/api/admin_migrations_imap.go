// IMAP mail-migration admin endpoints (GH #390 Google Workspace / #374
// M365, phase A step 4b). Unlike the cpmove-tarball sources handled in
// admin_migrations.go, IMAP is per-mailbox: each account carries its
// own remote login + app-password, and the message FETCH runs inside
// panel-api (internal/migrate/imap) rather than in the agent — there
// is no agent verb for pulling a remote IMAP account. So the run
// endpoint launches the fetch+import in a detached goroutine and
// returns 202; the operator polls GET /admin/migrations/:id for state.
//
// Routes (mounted by RegisterAdminMigrationRoutes, admin-gated):
//
//	POST /admin/migrations/imap/probe   enumerate remote folders (sync)
//	POST /admin/migrations/imap         start a migration (202 + goroutine)
//
// The app-password is used transiently and never persisted (no secret
// file): a panel-api restart mid-run drops the in-memory password, and
// the operator re-POSTs to resume — safe because the import de-dupes on
// Message-ID.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/imap"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// imapRunTimeout bounds a single account's fetch+import. Matches the
// fetch package's own ceiling; a real Workspace/M365 mailbox pull can
// run for hours.
const imapRunTimeout = 6 * time.Hour

// imapProbeFn / imapImportFn are seams so handler tests drive the
// endpoints without a live TLS IMAP transport. Production wiring uses
// the real package functions.
var (
	imapProbeFn  = imap.Probe
	imapImportFn = imap.Import
)

// imapConnRequest is the shared connection shape for probe + run.
type imapConnRequest struct {
	Host         string `json:"host" binding:"required"`
	Port         int    `json:"port"`
	STARTTLS     bool   `json:"starttls"`
	User         string `json:"user" binding:"required"`
	Password     string `json:"password" binding:"required"`
	AllowPrivate bool   `json:"allow_private"`
}

func (r imapConnRequest) probeConfig() imap.ProbeConfig {
	return imap.ProbeConfig{
		Host: r.Host, Port: r.Port, STARTTLS: r.STARTTLS,
		Username: r.User, Password: r.Password, AllowPrivate: r.AllowPrivate,
	}
}

// probeIMAP — POST /admin/migrations/imap/probe. Enumerates the remote
// account's folders + roles so the wizard can preview the mapping.
// Synchronous (LIST is fast). Auth failures are distinguished from
// connect failures, and the remote banner is never echoed to the
// client.
func (h *adminMigrationsHandler) probeIMAP(c *gin.Context) {
	var req imapConnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	res, err := imapProbeFn(ctx, req.probeConfig())
	if err != nil {
		code, hint := imap.Classify(err)
		// GH #1429: log the REAL error server-side (never the password) so the
		// operator can actually diagnose a failed probe. The client only ever
		// sees the safe hint + code — the remote banner is never echoed.
		slog.WarnContext(ctx, "imap migration probe failed",
			"host", req.Host, "port", req.Port, "starttls", req.STARTTLS,
			"user", req.User, "code", code, "err", err)
		status := http.StatusBadGateway
		if code == "auth_failed" {
			status = http.StatusUnauthorized
		}
		c.JSON(status, gin.H{"error": code, "detail": hint})
		return
	}
	c.JSON(http.StatusOK, gin.H{"folders": res.Folders, "total": len(res.Folders)})
}

// imapRunRequest adds the destination mailbox to the connection shape.
type imapRunRequest struct {
	imapConnRequest
	To string `json:"to" binding:"required"` // local jabali mailbox (must exist)
}

// runIMAP — POST /admin/migrations/imap. Creates/reuses the
// migration_jobs row and launches the fetch+import in a detached
// goroutine, returning 202 with the job id. The operator polls
// GET /admin/migrations/:id for progress.
func (h *adminMigrationsHandler) runIMAP(c *gin.Context) {
	var req imapRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	if h.cfg.Settings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "settings_unconfigured"})
		return
	}
	ctx := c.Request.Context()

	// Reuse the natural-key row; refuse if a run is already in flight.
	var jobID string
	if ex, _ := h.cfg.Jobs.FindBySource(ctx, models.MigrationSourceIMAP, req.Host, req.User); ex != nil {
		if ex.State == models.MigrationStateRestoring {
			c.JSON(http.StatusConflict, gin.H{"error": "already_running", "existing_job_id": ex.ID})
			return
		}
		jobID = ex.ID
	} else {
		row := &models.MigrationJob{
			ID:         genULID(),
			SourceKind: models.MigrationSourceIMAP,
			SourceHost: req.Host,
			SourceUser: req.User,
			State:      models.MigrationStatePending,
		}
		if err := h.cfg.Jobs.Create(ctx, row); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}
		jobID = row.ID
	}

	stagingBase := migrate.MigrationsRoot(ctx, h.cfg.Settings)
	if err := h.cfg.Jobs.UpdateState(ctx, jobID, models.MigrationStateRestoring, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}

	// Detach: the request context dies when this handler returns, so
	// the long fetch+import runs under a fresh bounded context. The
	// app-password lives only in this closure.
	go runIMAPImport(h.cfg.Jobs, h.cfg.Agent, imap.ImportConfig{
		Account:     req.probeConfig(),
		TargetEmail: req.To,
		JobID:       jobID,
		StagingBase: stagingBase,
	})

	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "state": models.MigrationStateRestoring})
}

// runIMAPImport runs one account's fetch+import to completion and
// records the terminal state. Runs in its own goroutine, detached from
// any request. Package-level (not a closure) so the seam imapImportFn
// is exercised and the goroutine body stays testable.
func runIMAPImport(jobs repository.MigrationJobRepository, agentCli agent.AgentInterface, cfg imap.ImportConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), imapRunTimeout)
	defer cancel()

	if _, err := imapImportFn(ctx, cfg, agentCli); err != nil {
		msg := err.Error()
		_ = jobs.UpdateState(context.Background(), cfg.JobID, models.MigrationStateFailed, &msg)
		return
	}
	_ = jobs.UpdateState(context.Background(), cfg.JobID, models.MigrationStateDone, nil)
}
