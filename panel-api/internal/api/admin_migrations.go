// M35 admin REST endpoints — read-only for now (list/get/stages).
// Mutation endpoints (create/cancel/resume) land alongside the
// admin UI Step 8 once the JMAP-push + CreateUser orchestrator
// pieces stabilise. v1 ships read-only because operators can
// already create + run jobs via the cobra CLI (jabali migrate
// import); the REST adds a UI-friendly observation surface.
//
// Routes mounted under /admin/migrations:
//
//	GET    /admin/migrations                  list (paginated envelope)
//	POST   /admin/migrations                  create (state=pending; runner
//	                                          stays operator-driven via CLI)
//	GET    /admin/migrations/:id              one job + recent stages
//	GET    /admin/migrations/:id/stages       full stage timeline
//	DELETE /admin/migrations/:id              soft-revoke (state→cancelled)
//	POST   /admin/migrations/:id/destroy      hard-delete row + secret + extracted dir
//	                                          (requires terminal state)
//
// Admin-gated. RequireAdmin already mounted by the parent group.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AdminMigrationsHandlerConfig is the dep set the routes need.
// Jobs is required; nil disables route registration. Agent is
// required for the three drive endpoints (secrets / pull-source /
// import); when nil those three endpoints register but 503 because
// the agent socket isn't reachable.
type AdminMigrationsHandlerConfig struct {
	Jobs      repository.MigrationJobRepository
	SizeCache repository.MigrationAccountSizeCacheRepository
	Settings  repository.ServerSettingsRepository
	Agent     agent.AgentInterface
}

// RegisterAdminMigrationRoutes mounts /admin/migrations* on g.
// g must already enforce RequireAuth + RequireAdmin upstream.
func RegisterAdminMigrationRoutes(g *gin.RouterGroup, cfg AdminMigrationsHandlerConfig) {
	if cfg.Jobs == nil {
		return
	}
	h := &adminMigrationsHandler{cfg: cfg}
	rg := g.Group("/admin/migrations")
	rg.Use(middleware.RequireAdmin())
	rg.GET("", h.list)
	rg.POST("", h.create)
	rg.GET("/:id", h.get)
	rg.GET("/:id/stages", h.stages)
	rg.DELETE("/:id", h.cancel)
	rg.POST("/:id/destroy", h.destroy)
	rg.POST("/refresh", h.refresh) // GH #646
	// GH #390/#374 phase A — per-mailbox IMAP source. probe enumerates
	// remote folders; the run endpoint launches fetch+import async.
	rg.POST("/imap/probe", h.probeIMAP)
	rg.POST("/imap", h.runIMAP)
	// SPA-driven migration endpoints. Each writes/launches via the
	// agent (transient systemd unit pattern, M29 §updates) so the
	// long-running pull + import survive panel-api restarts.
	rg.POST("/:id/secrets", h.uploadSecrets)
	rg.POST("/:id/test-connection", h.testConnection)   // GH #665
	rg.PUT("/:id/plan", h.updatePlan)                   // GH #665
	rg.POST("/:id/describe-account", h.describeAccount) // GH #665
	rg.POST("/:id/pull-source", h.runPullSource)
	rg.POST("/:id/import", h.runImport)
	rg.POST("/:id/import-wp", h.runImportWP) // GH #647 wordpress_ssh
	// WHM pkgacct (offline) flow: operator uploads a pre-built tarball
	// rather than pulling over SSH. Streams directly into the staging
	// directory so multi-GB pkgacct dumps don't double-buffer through
	// /tmp.
	rg.POST("/:id/tarball", h.uploadTarball)
	rg.GET("/:id/tarball", h.tarballStatus)

	// ADR-0095 wizard endpoints.
	rg.PATCH("/:id", h.patchDraft)
	rg.POST("/bulk", h.bulkCreate)
	rg.DELETE("/batches/:id", h.cancelBatch)
	rg.POST("/:id/retry", h.retry)
	rg.POST("/:id/submit", h.submitDraft)
	rg.GET("/:id/stream", h.stream)
	rg.GET("/discover-accounts/:host/:user/size", h.discoverAccountSize)
	rg.GET("/:id/discover-accounts", h.discoverAccounts)
	rg.GET("/:id/account-size/:user", h.accountSizeProbe)
}

type adminMigrationsHandler struct{ cfg AdminMigrationsHandlerConfig }

type migrationJobListResponse struct {
	Data     []models.MigrationJob `json:"data"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

func (h *adminMigrationsHandler) list(c *gin.Context) {
	page, pageSize := paginationParams(c)
	rows, total, err := h.cfg.Jobs.List(c.Request.Context(), page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if rows == nil {
		rows = []models.MigrationJob{}
	}
	c.JSON(http.StatusOK, migrationJobListResponse{
		Data: rows, Total: total, Page: page, PageSize: pageSize,
	})
}

// migrationJobDetailResponse bundles the job header + the most
// recent stage rows so the UI can render the timeline without
// a follow-up call.
type migrationJobDetailResponse struct {
	Job    *models.MigrationJob    `json:"job"`
	Stages []models.MigrationStage `json:"stages"`
}

func (h *adminMigrationsHandler) get(c *gin.Context) {
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	stages, err := h.cfg.Jobs.ListStages(c.Request.Context(), job.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if stages == nil {
		stages = []models.MigrationStage{}
	}
	c.JSON(http.StatusOK, migrationJobDetailResponse{Job: job, Stages: stages})
}

func (h *adminMigrationsHandler) stages(c *gin.Context) {
	stages, err := h.cfg.Jobs.ListStages(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if stages == nil {
		stages = []models.MigrationStage{}
	}
	c.JSON(http.StatusOK, gin.H{"data": stages, "total": len(stages)})
}

// createMigrationRequest is the wire shape for POST /admin/migrations.
// State defaults to "pending" for backward compat; wizard flows pass
// "draft" so the row can be incrementally populated via PATCH before
// the runner picks it up. ADR-0095 decision 5.
type createMigrationRequest struct {
	SourceKind string `json:"source_kind" binding:"required"`
	SourceHost string `json:"source_host"`
	SourceUser string `json:"source_user" binding:"required"`
	State      string `json:"state,omitempty"`
	// ExpectedHostKey — optional SHA256 fingerprint of the source SSH host key
	// (GH #461). When set, the connector verifies the host key against it.
	ExpectedHostKey string `json:"expected_host_key,omitempty"`
	// SourcePath (GH #647 wordpress_ssh) — the WP root on the source.
	SourcePath string `json:"source_path,omitempty"`
}

// create inserts a fresh migration_jobs row with state='pending'.
// Does NOT kick off the runner — runner stays operator-driven via
// the cobra CLI ('jabali migrate import') so a misclick on the SPA
// can't trigger a 30-min restore against an unprepared destination.
//
// Validates source_kind against the registered importers + the
// offline whm_pkgacct kind so the operator can't create a row
// for an unknown kind that the runner would later reject.
func (h *adminMigrationsHandler) create(c *gin.Context) {
	var req createMigrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if !isKnownSourceKind(req.SourceKind) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "unknown_source_kind",
			"detail": "supported kinds: cpanel, whm_pkgacct, directadmin, hestiacp",
		})
		return
	}
	if !validMigrationFingerprint(req.ExpectedHostKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_fingerprint", "detail": "expected_host_key must be a SHA256 fingerprint (optionally SHA256:-prefixed)"})
		return
	}
	// Refuse duplicates on the natural-key tuple — the UNIQUE on
	// (source_host, source_user, source_kind) would 500 the
	// gorm.Create otherwise.
	//
	// EXCEPTION (ADR-0095 decision 5 wizard idempotency): when the
	// caller is asking for state=draft AND the existing row IS also
	// draft, return the existing row as 200 OK instead of 409. The
	// wizard hits this when its placeholder generation collides with
	// a prior stranded draft (rare but observed on mx.jabali-panel
	// .local 2026-05-12). Operators on the existing-row path can
	// always cancel-then-recreate explicitly.
	if existing, _ := h.cfg.Jobs.FindBySource(c.Request.Context(),
		req.SourceKind, req.SourceHost, req.SourceUser); existing != nil {
		if req.State == models.MigrationStateDraft && existing.State == models.MigrationStateDraft {
			c.JSON(http.StatusOK, existing)
			return
		}
		c.JSON(http.StatusConflict, gin.H{
			"error":           "duplicate_migration_job",
			"existing_job_id": existing.ID,
			"detail":          "A migration job already exists for this (source_host, source_user, source_kind). Resume via 'jabali migrate import --job-id ...' instead of recreating.",
		})
		return
	}

	state := models.MigrationStatePending
	if req.State == models.MigrationStateDraft {
		state = models.MigrationStateDraft
	}
	row := &models.MigrationJob{
		ID:              genULID(),
		SourceKind:      req.SourceKind,
		SourceHost:      req.SourceHost,
		SourceUser:      req.SourceUser,
		State:           state,
		ExpectedHostKey: strings.TrimSpace(req.ExpectedHostKey),
	}
	if sp := strings.TrimSpace(req.SourcePath); sp != "" { // GH #647 wordpress_ssh
		row.SourcePath = &sp
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, row)
}

// validMigrationFingerprint accepts an empty value (TOFU) or a SHA256 host-key
// fingerprint: an optional "SHA256:" prefix over 43 base64 chars (Gitea #461).
var migrationFingerprintRe = regexp.MustCompile(`^(SHA256:)?[A-Za-z0-9+/]{43}=?$`)

func validMigrationFingerprint(fp string) bool {
	fp = strings.TrimSpace(fp)
	return fp == "" || migrationFingerprintRe.MatchString(fp)
}

// isKnownSourceKind gates against the closed set of source-kinds
// the runner currently understands. Importer registry would also
// answer this, but offline kinds (whm_pkgacct) aren't registered
// — explicit allow-list is clearer.
func isKnownSourceKind(s string) bool {
	switch s {
	case models.MigrationSourceCpanel,
		models.MigrationSourceWHMpkgacct,
		models.MigrationSourceDirectAdmin,
		models.MigrationSourceHestia,
		models.MigrationSourceWordPressSSH,    // GH #647
		models.MigrationSourceWordPressPlugin, // GH #648
		models.MigrationSourceIMAP,            // GH #390/#374
		models.MigrationSourcePlesk:           // GH #429
		return true
	}
	return false
}

// genULID is hoisted so tests can swap in a deterministic ULID
// generator without monkey-patching ids.NewULID at package level.
var genULID = ulidNew

// cancel soft-revokes a job — transitions to MigrationStateCancelled
// when the current state allows. Already-terminal jobs (done /
// failed / cancelled) return 409 so the operator knows the row was
// never advanced.
//
// NOTE: cancel does NOT kill an in-flight `jabali migrate import`
// process. v1 transient-unit invoker is the operator's cobra
// shell; cancelling at the panel layer just stamps the DB row.
// A follow-up agent-level kill switch is M35 Step 8 follow-up.
func (h *adminMigrationsHandler) cancel(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !migrate.IsValidJobTransition(job.State, models.MigrationStateCancelled) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "illegal_transition",
			"detail": "job in state '" + job.State + "' cannot be cancelled (already terminal?)",
		})
		return
	}
	reason := "cancelled by admin"
	if err := h.cfg.Jobs.UpdateState(c.Request.Context(), id, models.MigrationStateCancelled, &reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Same immediate-secrets-wipe as the runner's terminal paths
	// (ADR-0094). Best-effort: failure to wipe surfaces in the
	// daily reaper's next sweep.
	_ = migrate.WipeJobSecret(id)
	c.JSON(http.StatusOK, gin.H{"id": id, "state": models.MigrationStateCancelled})
}

// destroy hard-deletes a terminal-state migration_jobs row + wipes
// the secrets file + removes /var/lib/jabali-migrations/<id>/.
// Refuses non-terminal jobs to prevent accidental destruction of an
// in-flight migration.
//
// Three side effects on success:
//   - migration_jobs row deleted (FK CASCADE drops migration_stages)
//   - /etc/jabali-panel/migration-secrets/<id>.env removed if present
//   - /var/lib/jabali-migrations/<id>/ removed (extracted tarball,
//     downloaded source tar, etc.)
//
// Each is best-effort: failure to wipe the FS dir doesn't roll back
// the DB delete (operator can rm -rf the dir manually). Failure to
// delete the DB row does fail the request.
func (h *adminMigrationsHandler) destroy(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	switch job.State {
	case models.MigrationStateDone, models.MigrationStateFailed, models.MigrationStateCancelled, models.MigrationStateDraft:
		// allowed — drafts have no extracted dir or secret to clean
		// up via cancel-first; hard-delete is the right semantic
		// (ADR-0095 decision 5 wizard "Discard" path).
	default:
		c.JSON(http.StatusConflict, gin.H{
			"error":  "non_terminal",
			"detail": "destroy refused: cancel the job first (DELETE /admin/migrations/" + id + ") to transition to terminal state",
		})
		return
	}
	// DB row first — FK CASCADE drops migration_stages.
	if err := h.cfg.Jobs.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	// Filesystem side-effects best-effort. RemoveAll is OK to call
	// on a non-existent path; doesn't error.
	_ = migrate.WipeJobSecret(id)
	stagingDir := "/var/lib/jabali-migrations/" + id
	_ = os.RemoveAll(stagingDir)
	c.JSON(http.StatusOK, gin.H{"id": id, "destroyed": true})
}

// paginationParams pulls ?page= + ?page_size= with sane defaults.
// Reused shape from M44 + M30 admin endpoints; not extracted into
// a shared helper because each handler tunes its own caps.
func paginationParams(c *gin.Context) (page, pageSize int) {
	page = 1
	pageSize = 50
	if v := c.Query("page"); v != "" {
		if n, err := atoiNonNeg(v); err == nil && n > 0 {
			page = n
		}
	}
	if v := c.Query("page_size"); v != "" {
		if n, err := atoiNonNeg(v); err == nil && n > 0 && n <= 200 {
			pageSize = n
		}
	}
	return page, pageSize
}

// ulidNew defaults genULID to the panel's canonical ULID generator.
// Kept as a package-level var (rather than a direct ids.NewULID
// reference inline) so test paths can override.
func ulidNew() string {
	return ids.NewULID()
}

// uploadSecretsRequest — operator picks ONE of password / private-key
// per ADR-0094. UI presents a tabbed form; either tab posts here.
type uploadSecretsRequest struct {
	SSHPassword   string `json:"ssh_password"`
	SSHPrivateKey string `json:"ssh_private_key"`
	PluginToken   string `json:"plugin_token"` // GH #648 wordpress_plugin
}

// uploadSecrets — POST /admin/migrations/:id/secrets writes the per-job
// env file via the agent (root-owned 0640). Job must exist + be in a
// non-terminal state. Empty payload (neither password nor key) → 400.
func (h *adminMigrationsHandler) uploadSecrets(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if isTerminalMigrationState(job.State) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "terminal_state",
			"detail": "cannot upload secrets to a terminal job (state=" + job.State + ")",
		})
		return
	}
	var req uploadSecretsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if req.SSHPassword == "" && req.SSHPrivateKey == "" && req.PluginToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "missing_credential",
			"detail": "ssh_password, ssh_private_key, or plugin_token required",
		})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	h.callAgent(c, "migration.secrets_write", map[string]any{
		"job_id":          id,
		"ssh_password":    req.SSHPassword,
		"ssh_private_key": req.SSHPrivateKey,
		"plugin_token":    req.PluginToken,
	}, 10*time.Second)
}

// runPullSourceRequest — defaults: ssh_user="root".
type runPullSourceRequest struct {
	SSHUser string `json:"ssh_user"`
}

// runPullSource — POST /admin/migrations/:id/pull-source kicks off
// `jabali migrate pull-source --job-id` under a transient systemd
// unit. Refuses if the job has no manifest (manifest = source-side
// discovery output; pull-source assumes discovery already ran). State
// must be pending or pulling_failed.
// updatePlan stores the wizard's migration plan (selected accounts + import-area
// toggles) as JSON on the job. The import honors it (absent = import all). #665.
func (h *adminMigrationsHandler) updatePlan(c *gin.Context) {
	id := c.Param("id")
	if _, err := h.cfg.Jobs.FindByID(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<16))
	if err != nil || len(body) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty_plan"})
		return
	}
	if !json.Valid(body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	if err := h.cfg.Jobs.UpdatePlan(c.Request.Context(), id, string(body)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *adminMigrationsHandler) runPullSource(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if isTerminalMigrationState(job.State) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "terminal_state",
			"detail": "cannot pull a terminal job (state=" + job.State + ")",
		})
		return
	}
	var req runPullSourceRequest
	_ = c.ShouldBindJSON(&req) // body optional
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	h.callAgent(c, "migration.pull_source_run", map[string]any{
		"job_id":   id,
		"ssh_user": req.SSHUser,
	}, 10*time.Second)
}

// runImportWPRequest — GH #647. dest_user + dest_domain the site imports into.
type runImportWPRequest struct {
	DestUser   string `json:"dest_user" binding:"required"`
	DestDomain string `json:"dest_domain" binding:"required"`
}

// runImportWP triggers `jabali migrate import-wp` under a transient unit for a
// staged wordpress_ssh job (GH #647). Owner-scoping of the dest is enforced by
// the create path; this only launches the already-bound job.
func (h *adminMigrationsHandler) runImportWP(c *gin.Context) {
	id := c.Param("id")
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	var req runImportWPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dest_user and dest_domain are required"})
		return
	}
	h.callAgent(c, "migration.import_wp_run", map[string]any{
		"job_id":      id,
		"dest_user":   req.DestUser,
		"dest_domain": req.DestDomain,
	}, 10*time.Second)
}

// runImportRequest — TargetUser is required; the rest are optional.
// When TargetEmail/TargetPassword are present + the user doesn't yet
// exist, the import command auto-creates them (per M35 auto-create
// flow added in 91ba51a9).
type runImportRequest struct {
	TargetUser      string `json:"target_user" binding:"required"`
	TargetEmail     string `json:"target_email"`
	TargetPassword  string `json:"target_password"`
	TargetPackageID string `json:"target_package_id"`
}

func (h *adminMigrationsHandler) runImport(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if isTerminalMigrationState(job.State) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "terminal_state",
			"detail": "cannot import a terminal job (state=" + job.State + ")",
		})
		return
	}
	var req runImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	h.callAgent(c, "migration.import_run", map[string]any{
		"job_id":            id,
		"target_user":       req.TargetUser,
		"target_email":      req.TargetEmail,
		"target_password":   req.TargetPassword,
		"target_package_id": req.TargetPackageID,
	}, 10*time.Second)
}

// callAgent — copy of admin_updates.go pattern. Hoisted as a method
// so both add a context-deadline + uniform error envelope.
func (h *adminMigrationsHandler) callAgent(c *gin.Context, cmd string, params any, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, cmd, params)
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	c.JSON(http.StatusOK, data)
}

// isTerminalMigrationState — done / failed / cancelled. Prevents
// re-running pull or import on a terminal job; operator must
// destroy + recreate.
func isTerminalMigrationState(s string) bool {
	switch s {
	case models.MigrationStateDone, models.MigrationStateFailed, models.MigrationStateCancelled:
		return true
	}
	return false
}

// atoiNonNeg is strconv.Atoi with a non-negative refusal so a
// negative `page` doesn't underflow the offset calculation in the
// repo.
func atoiNonNeg(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("non-digit")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// migrationStagingDir returns the per-job staging dir used by both
// SSH-pull (jabali migrate pull-source) and the offline tarball-
// upload paths. Mirrored from migrate_pull_cmd.go so the SPA hits
// the same convention the cobra runner expects.
func migrationStagingDir(jobID string) string {
	return "/var/lib/jabali-migrations/" + jobID
}

// uploadTarball — POST /admin/migrations/:id/tarball streams a
// multipart/form-data 'file' part directly into the staging dir as
// source.tar.gz (root:jabali-readable, panel-api owns the dir per
// install.sh:2849). Streams via MultipartReader rather than
// ParseMultipartForm so a multi-GB pkgacct dump doesn't double-
// buffer through /tmp.
//
// Wire shape:
//
//	POST /admin/migrations/<id>/tarball
//	Content-Type: multipart/form-data; boundary=...
//	file: <pkgacct-cpmove-foo.tar.gz>
//
// Response: {path, size_bytes, sha256_truncated}.
//
// Refuses if job is in a terminal state OR an existing tarball is
// already present (operator must DELETE /admin/migrations/<id> +
// recreate, or destroy the job, to retry — re-uploading on top of
// an extracted source dir would fight the runner).
func (h *adminMigrationsHandler) uploadTarball(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if isTerminalMigrationState(job.State) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "terminal_state",
			"detail": "cannot upload tarball to a terminal job (state=" + job.State + ")",
		})
		return
	}
	staging := migrationStagingDir(id)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mkdir_staging", "detail": err.Error()})
		return
	}
	target := filepath.Join(staging, "source.tar.gz")
	if _, err := os.Stat(target); err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "tarball_exists",
			"detail": "destroy the job (POST /admin/migrations/" + id + "/destroy after cancel) to clear the staging dir before re-uploading",
		})
		return
	}

	// Streaming upload — MultipartReader returns parts one-by-one so
	// nothing buffers in memory or /tmp. 20 GiB cap on Body matches
	// the upper bound of a typical cPanel full-backup tarball; smaller
	// pkgacct dumps stream the same way.
	const maxUploadBytes = 20 * 1024 * 1024 * 1024
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_multipart", "detail": err.Error()})
		return
	}
	var (
		wrote   int64
		wrotten = false
	)
	for {
		part, perr := reader.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "multipart_read", "detail": perr.Error()})
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		tmp := target + ".part"
		dst, oerr := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if oerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "open_tmp", "detail": oerr.Error()})
			return
		}
		n, cerr := io.Copy(dst, part)
		_ = part.Close()
		if cerr != nil {
			_ = dst.Close()
			_ = os.Remove(tmp)
			// MaxBytesReader hit returns http: request body too large;
			// surface it as 413 so the client knows to split or compress.
			if cerr.Error() == "http: request body too large" {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{
					"error":     "tarball_too_large",
					"max_bytes": maxUploadBytes,
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "stream_failed", "detail": cerr.Error()})
			return
		}
		if err := dst.Close(); err != nil {
			_ = os.Remove(tmp)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "close_tmp", "detail": err.Error()})
			return
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "rename", "detail": err.Error()})
			return
		}
		wrote = n
		wrotten = true
		break
	}
	if !wrotten {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_file_field", "detail": "expected one multipart part named 'file'"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"path":       target,
		"size_bytes": wrote,
	})
}

// tarballStatus — GET /admin/migrations/:id/tarball reports whether a
// tarball is staged + its size. SPA uses this to drive the upload-vs-
// re-upload UI state on the detail page.
func (h *adminMigrationsHandler) tarballStatus(c *gin.Context) {
	id := c.Param("id")
	if _, err := h.cfg.Jobs.FindByID(c.Request.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	target := filepath.Join(migrationStagingDir(id), "source.tar.gz")
	st, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"present": false})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stat", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"present":    true,
		"path":       target,
		"size_bytes": st.Size(),
		"mtime":      st.ModTime().UTC().Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------------------
// ADR-0095 wizard endpoints
// ---------------------------------------------------------------------------

type patchDraftRequest struct {
	SourceHost      *string `json:"source_host,omitempty"`
	SourceUser      *string `json:"source_user,omitempty"`
	TargetUserID    *string `json:"target_user_id,omitempty"`
	ExpectedHostKey *string `json:"expected_host_key,omitempty"`
}

// patchDraft updates a draft-state job. Allows the wizard to keep the
// row in sync as the operator moves through steps 2-4. PATCH on a job
// in any non-draft state returns 409.
func (h *adminMigrationsHandler) patchDraft(c *gin.Context) {
	id := c.Param("id")
	var req patchDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if req.ExpectedHostKey != nil && !validMigrationFingerprint(*req.ExpectedHostKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_fingerprint", "detail": "expected_host_key must be a SHA256 fingerprint (optionally SHA256:-prefixed)"})
		return
	}
	if err := h.cfg.Jobs.PatchDraft(c.Request.Context(), id, req.SourceHost, req.SourceUser, req.TargetUserID, req.ExpectedHostKey); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusConflict, gin.H{"error": "not_draft", "detail": "job missing or not in draft state"})
			return
		}
		if errors.Is(err, repository.ErrConflict) {
			// Another migration_job already owns this (host, user, kind)
			// tuple. Look up the existing row so the wizard UI can
			// offer "switch to existing draft" instead of forcing the
			// operator to dig through the list page.
			var existingID string
			currentJob, _ := h.cfg.Jobs.FindByID(c.Request.Context(), id)
			if currentJob != nil && req.SourceHost != nil && req.SourceUser != nil {
				if existing, _ := h.cfg.Jobs.FindBySource(c.Request.Context(),
					currentJob.SourceKind, *req.SourceHost, *req.SourceUser); existing != nil && existing.ID != id {
					existingID = existing.ID
				}
			}
			body := gin.H{
				"error":  "host_user_kind_in_use",
				"detail": "another migration job already owns this (source_host, source_user, source_kind). Switch to the existing draft or pick a different host/user.",
			}
			if existingID != "" {
				body["existing_job_id"] = existingID
			}
			c.JSON(http.StatusConflict, body)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	row, _ := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	c.JSON(http.StatusOK, row)
}

type bulkCreateRequest struct {
	SourceKind string   `json:"source_kind" binding:"required"`
	SourceHost string   `json:"source_host" binding:"required"`
	Accounts   []string `json:"accounts"    binding:"required"`
	// AccountTargets (GH #665) — optional source_user -> existing target_user_id
	// map. Absent/empty for an account = create a new Jabali user (default).
	AccountTargets map[string]string `json:"account_targets,omitempty"`
	// SourceJobID — optional. When set, the bulk handler copies the
	// referenced job's secret env-file onto every newly created job
	// + flips them to pending + auto-kicks pull-source. This is the
	// wizard's "discover → select → restore" path: the discovery
	// draft owns the SSH credentials; the bulk children inherit
	// them so the operator doesn't re-upload N times. Without this,
	// jobs land as drafts the operator must resume one-by-one.
	SourceJobID string `json:"source_job_id"`
}

type bulkCreateResponse struct {
	BatchID string                `json:"batch_id"`
	Jobs    []models.MigrationJob `json:"jobs"`
}

// bulkCreate fires off N draft jobs sharing a batch_id — one per
// account name in `accounts`. Used by the WHM wizard after Step 3
// account selection.
func (h *adminMigrationsHandler) bulkCreate(c *gin.Context) {
	var req bulkCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if !isKnownSourceKind(req.SourceKind) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_source_kind"})
		return
	}
	if len(req.Accounts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_accounts", "detail": "accounts[] required"})
		return
	}
	// When SourceJobID is set the bulk handler clones the source
	// draft's secret env-file onto every child via the agent's
	// migration.secrets_clone command (panel-api runs as jabali —
	// cannot write to root-owned secrets dir). Empty falls back to
	// legacy draft-create behavior so any caller not yet using the
	// inheritance path keeps working.
	inheritSecrets := req.SourceJobID != "" && h.cfg.Agent != nil
	if inheritSecrets {
		srcSecret := filepath.Join(migrate.SecretsDir, req.SourceJobID+".env")
		if _, statErr := os.Stat(srcSecret); statErr != nil {
			c.JSON(http.StatusPreconditionFailed, gin.H{
				"error":  "source_secret_missing",
				"detail": "no secret found for source_job_id=" + req.SourceJobID + " — re-upload SSH credentials in the wizard's Connection step before clicking Continue.",
			})
			return
		}
	}

	finalState := models.MigrationStateDraft
	if inheritSecrets {
		finalState = models.MigrationStatePending
	}

	batchID := genULID()
	out := make([]models.MigrationJob, 0, len(req.Accounts))
	createdIDs := make([]string, 0, len(req.Accounts))

	cloneCtx, cancelClone := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancelClone()
	for _, acct := range req.Accounts {
		row := &models.MigrationJob{
			ID:         genULID(),
			BatchID:    &batchID,
			SourceKind: req.SourceKind,
			SourceHost: req.SourceHost,
			SourceUser: acct,
			State:      finalState,
		}
		if req.AccountTargets != nil {
			if t := strings.TrimSpace(req.AccountTargets[acct]); t != "" {
				row.TargetUserID = &t // map to existing user; else auto-create
			}
		}
		if err := h.cfg.Jobs.Create(c.Request.Context(), row); err != nil {
			// Skip dupes silently — operator re-selected an account
			// that already has an existing job under this host.
			continue
		}
		if inheritSecrets {
			if _, err := h.cfg.Agent.Call(cloneCtx, "migration.secrets_clone", map[string]any{
				"src_job_id": req.SourceJobID,
				"dst_job_id": row.ID,
			}); err != nil {
				// Demote to draft so the operator can hand-resume
				// rather than leaving a pending job without a secret.
				_ = h.cfg.Jobs.UpdateState(c.Request.Context(), row.ID, models.MigrationStateDraft, nil)
				row.State = models.MigrationStateDraft
			}
		}
		out = append(out, *row)
		if row.State == models.MigrationStatePending {
			createdIDs = append(createdIDs, row.ID)
		}
	}
	// Auto-kick pull-source for each pending child. Same dispatch
	// the single-account /submit handler uses.
	if h.cfg.Agent != nil && len(createdIDs) > 0 {
		kickCtx, cancelKick := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancelKick()
		for _, id := range createdIDs {
			_, _ = h.cfg.Agent.Call(kickCtx, "migration.pull_source_run", map[string]any{
				"job_id":   id,
				"ssh_user": "root",
			})
		}
	}
	c.JSON(http.StatusCreated, bulkCreateResponse{BatchID: batchID, Jobs: out})
}

// cancelBatch transitions every job in the batch to cancelled. Uses
// the existing cancel semantics per job (soft-revoke).
func (h *adminMigrationsHandler) cancelBatch(c *gin.Context) {
	batchID := c.Param("id")
	jobs, err := h.cfg.Jobs.ListByBatch(c.Request.Context(), batchID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	if len(jobs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "batch_not_found"})
		return
	}
	cancelled := 0
	for _, job := range jobs {
		// Skip terminal-state jobs; cancel only live ones.
		switch job.State {
		case models.MigrationStateDone, models.MigrationStateFailed,
			models.MigrationStateCancelled:
			continue
		}
		if err := h.cfg.Jobs.UpdateState(c.Request.Context(), job.ID, models.MigrationStateCancelled, nil); err == nil {
			cancelled++
		}
	}
	c.JSON(http.StatusOK, gin.H{"batch_id": batchID, "cancelled": cancelled, "total": len(jobs)})
}

// retry re-runs the failed job. Default = resume from last-good stage
// (?from_scratch=false). With ?from_scratch=true, wipes prior stage
// rows + the extracted/ directory and starts from analyze.
//
// Phase-3 backend follow-up: actual stage idempotency audit + extracted
// dir cleanup is gated on the runner. For now this endpoint flips the
// job state back to pending + lets the existing pull-source / import
// flow re-run; full-restart variant clears stage rows so the runner
// sees them as un-attempted.
func (h *adminMigrationsHandler) retry(c *gin.Context) {
	id := c.Param("id")
	fromScratch := c.Query("from_scratch") == "true"

	row, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if row.State != models.MigrationStateFailed && row.State != models.MigrationStateCancelled {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "not_retryable",
			"detail": "retry only valid in failed/cancelled state",
		})
		return
	}
	if fromScratch {
		// Wipe stage rows so the runner re-creates them from analyze.
		// extracted/ cleanup happens server-side via the runner's
		// "fresh-pull" guard (Phase 3 follow-up — for now the agent
		// re-extracts onto the existing dir which the cpanel parser
		// handles idempotently via tar -xkf).
		stages, _ := h.cfg.Jobs.ListStages(c.Request.Context(), id)
		for _, s := range stages {
			_ = h.cfg.Jobs.UpdateStage(c.Request.Context(), s.ID, models.MigrationStatePending, 0, nil)
		}
	}
	if err := h.cfg.Jobs.UpdateState(c.Request.Context(), id, models.MigrationStatePending, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	out, _ := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	c.JSON(http.StatusOK, gin.H{"job": out, "from_scratch": fromScratch})
}

// stream emits SSE updates while the job is non-terminal. Refresh
// cadence: 2s — matches the runner's typical stage-completion
// granularity without hammering the DB. Closes once terminal.
//
// ADR-0095 decision 4 — reuses nginx WS-proxy block already configured
// for /api/v1/logs/stream/* (proxy_buffering off, long timeouts).
func (h *adminMigrationsHandler) stream(c *gin.Context) {
	id := c.Param("id")

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy_buffering

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming_unsupported"})
		return
	}

	emit := func() (terminal bool) {
		job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
		if err != nil {
			fmt.Fprintf(c.Writer, "event: error\ndata: %q\n\n", err.Error())
			flusher.Flush()
			return true
		}
		stages, _ := h.cfg.Jobs.ListStages(c.Request.Context(), id)
		payload, _ := json.Marshal(gin.H{"job": job, "stages": stages})
		fmt.Fprintf(c.Writer, "event: snapshot\ndata: %s\n\n", payload)
		flusher.Flush()
		switch job.State {
		case models.MigrationStateDone,
			models.MigrationStateFailed,
			models.MigrationStateCancelled:
			return true
		}
		return false
	}

	if emit() {
		return // already terminal — single snapshot then close
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			if emit() {
				return
			}
		}
	}
}

// discoverAccountSize returns the size of one source account, hitting
// the (host, source_user) cache first with a 24h TTL. ADR-0095
// decision 6 — keeps Step 3 instant on re-discovery while letting the
// initial probe cost ~5s per account.
//
// Phase 4 follow-up: this handler currently returns 503 from_cache=false
// when the cache is cold — agent-side `du -sh /home/<user>` ssh probe
// is not yet wired. Operator can pre-warm the cache via the agent's
// system.disk_usage handler once the M35.2 wave adds the discoverer
// hook.
func (h *adminMigrationsHandler) discoverAccountSize(c *gin.Context) {
	host := c.Param("host")
	user := c.Param("user")
	if h.cfg.SizeCache == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "size_cache_not_configured"})
		return
	}
	row, err := h.cfg.SizeCache.Get(c.Request.Context(), host, user, 24*time.Hour)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{
			"host":        host,
			"source_user": user,
			"size_bytes":  row.SizeBytes,
			"fetched_at":  row.FetchedAt,
			"from_cache":  true,
		})
		return
	}
	// Cold cache. Live probe deferred to M35.2 (needs Discoverer
	// hook + per-source-kind du command). For now signal to the SPA
	// that the operator can either skip the size column or wait for
	// the M35.2 wave.
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"host":        host,
		"source_user": user,
		"from_cache":  false,
		"error":       "size_probe_not_wired",
		"detail":      "Live size probe ships in M35.2; pre-warm the cache via 'jabali migrate discover-size <host> <user>' (CLI) until then.",
	})
}

// discoverAccounts — GET /admin/migrations/:id/discover-accounts.
// Connects to the source via the existing per-job secret + Discoverer.
// Returns []migrate.AccountSummary (login/domain/email/bytes_total) —
// the SPA's account-picker step consumes this. ADR-0095 decision 3
// (bulk-WHM) + decision 6 (size column from cheap whmapi1 listaccts;
// per-account du-sh remains a separate lazy endpoint).
//
// Refuses unless job state ∈ {draft, pending} — once the runner has
// started no point re-listing source-side accounts.
func (h *adminMigrationsHandler) discoverAccounts(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if job.State != models.MigrationStateDraft && job.State != models.MigrationStatePending {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "wrong_state",
			"detail": "discover-accounts only valid in draft/pending; got " + job.State,
		})
		return
	}
	disc, err := migrate.Get(job.SourceKind)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_discoverer", "detail": err.Error()})
		return
	}
	// Apply the SSRF private-host override from server_settings so the
	// admin UI toggle takes effect on the next discover call without a
	// process restart. Best-effort: settings repo unset or row missing
	// falls back to the conservative default.
	if h.cfg.Settings != nil {
		if s, sErr := h.cfg.Settings.Get(c.Request.Context()); sErr == nil && s != nil {
			migrate.ApplyAllowPrivate(disc, s.MigrationAllowPrivateHosts)
		}
	}
	// Per-job secret file. Step 2 of the wizard POSTed it via
	// /:id/secrets; existence here is the operator's "I'm ready to
	// list accounts" gate.
	secretPath := filepath.Join(migrate.SecretsDir, job.ID+".env")
	if _, err := os.Stat(secretPath); err != nil {
		c.JSON(http.StatusPreconditionRequired, gin.H{
			"error":  "secret_missing",
			"detail": "POST /:id/secrets first",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	sess, err := disc.Connect(ctx, job.SourceHost, job.SourceUser, migrate.SecretRef{Path: secretPath, ExpectedHostKey: job.ExpectedHostKey})
	if err != nil {
		respondAgentErr(c, "connect_failed", err)
		return
	}
	defer func() { _ = disc.Close(ctx, sess) }()

	accounts, err := disc.ListAccounts(ctx, sess)
	if err != nil {
		respondAgentErr(c, "list_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"accounts": accounts,
		"total":    len(accounts),
	})
}

// submitDraft flips a draft row to pending. ADR-0095 decision 5
// closes the wizard loop: Step 4's "Submit" button on non-WHM flows
// (cpanel/directadmin/hestiacp) hits this endpoint to graduate the
// row out of draft. WHM bulk flows go via POST /bulk and never call
// here — bulk-create writes drafts that the operator submits per-
// account via the existing list page.
//
// Refuses any state other than draft (404 if missing, 409 if not
// draft). Validates that source_host + source_user are real values
// (the wizard seeds them with __draft_* placeholders at step 1 and
// PATCHes the actual values at step 2; refusing __draft_* prefixes
// catches the operator who tries to submit without completing
// the connection step).
func (h *adminMigrationsHandler) submitDraft(c *gin.Context) {
	id := c.Param("id")
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if job.State != models.MigrationStateDraft {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "not_draft",
			"detail": "submit only valid in draft state; got " + job.State,
		})
		return
	}
	if strings.HasPrefix(job.SourceHost, "__draft_") || strings.HasPrefix(job.SourceUser, "__draft_") {
		c.JSON(http.StatusPreconditionRequired, gin.H{
			"error":  "draft_incomplete",
			"detail": "PATCH source_host + source_user before submitting",
		})
		return
	}
	if err := h.cfg.Jobs.UpdateState(c.Request.Context(), id, models.MigrationStatePending, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	// Auto-kick the agent's pull-source runner for every SSH-based
	// kind. WHM is no longer offline-only: pull-source runs pkgacct
	// per account over the operator's WHM root SSH session (reuses
	// the cpanel SSH path). Operators who genuinely need the offline
	// tarball-upload path can still use the per-job /:id/tarball
	// endpoint.
	out, _ := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if h.cfg.Agent != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		if _, err := h.cfg.Agent.Call(ctx, "migration.pull_source_run", map[string]any{
			"job_id":   id,
			"ssh_user": "root",
		}); err != nil {
			c.JSON(http.StatusAccepted, gin.H{
				"job":          out,
				"pull_started": false,
				"pull_error":   err.Error(),
			})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"job": out, "pull_started": h.cfg.Agent != nil})
}

// accountSizeProbe is the cold-cache live SSH probe. ADR-0095 decision 6.
// Loads the draft job's secret, connects via Discoverer, asserts the
// Discoverer implements migrate.SizeProber, calls AccountSize, upserts
// the cache, returns the size.
//
// 5xx ladder:
//
//	501 Not Implemented — Discoverer for this source_kind doesn't
//	    implement SizeProber. Falls back to "discovery returned 0".
//	502 Bad Gateway     — SSH connect / du failed upstream.
//	412 Precondition    — secret missing (POST /:id/secrets first).
//	404 Not Found       — draft job missing.
//	409 Conflict        — job not in draft/pending state.
func (h *adminMigrationsHandler) accountSizeProbe(c *gin.Context) {
	id := c.Param("id")
	login := c.Param("user")

	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if job.State != models.MigrationStateDraft && job.State != models.MigrationStatePending {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "wrong_state",
			"detail": "account-size only valid in draft/pending; got " + job.State,
		})
		return
	}

	// Cache hit first — operator may double-click + size cache TTL is 24h.
	if h.cfg.SizeCache != nil {
		if row, cerr := h.cfg.SizeCache.Get(c.Request.Context(), job.SourceHost, login, 24*time.Hour); cerr == nil {
			c.JSON(http.StatusOK, gin.H{
				"host":        job.SourceHost,
				"source_user": login,
				"size_bytes":  row.SizeBytes,
				"fetched_at":  row.FetchedAt,
				"from_cache":  true,
			})
			return
		}
	}

	disc, err := migrate.Get(job.SourceKind)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_discoverer", "detail": err.Error()})
		return
	}
	if h.cfg.Settings != nil {
		if s, sErr := h.cfg.Settings.Get(c.Request.Context()); sErr == nil && s != nil {
			migrate.ApplyAllowPrivate(disc, s.MigrationAllowPrivateHosts)
		}
	}
	prober, ok := disc.(migrate.SizeProber)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{
			"error":  "size_probe_unsupported",
			"detail": "Discoverer for " + job.SourceKind + " does not implement SizeProber",
		})
		return
	}
	secretPath := filepath.Join(migrate.SecretsDir, job.ID+".env")
	if _, err := os.Stat(secretPath); err != nil {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "secret_missing"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	sess, err := disc.Connect(ctx, job.SourceHost, job.SourceUser, migrate.SecretRef{Path: secretPath, ExpectedHostKey: job.ExpectedHostKey})
	if err != nil {
		respondAgentErr(c, "connect_failed", err)
		return
	}
	defer func() { _ = disc.Close(ctx, sess) }()

	size, err := prober.AccountSize(ctx, sess, login)
	if err != nil {
		respondAgentErr(c, "probe_failed", err)
		return
	}

	if h.cfg.SizeCache != nil {
		_ = h.cfg.SizeCache.Upsert(c.Request.Context(), job.SourceHost, login, size)
	}
	c.JSON(http.StatusOK, gin.H{
		"host":        job.SourceHost,
		"source_user": login,
		"size_bytes":  size,
		"from_cache":  false,
	})
}

// refresh (GH #646) drives a guard-railed re-migration from the panel: backup
// FIRST, then force-mirror files (preserving jabali-managed files) + DB reimport
// + reconcile. Admin-gated, force-required, audited. Shares migrate.RunRefresh
// with the `jabali migrate refresh` CLI so the ordering + invariants are identical.
func (h *adminMigrationsHandler) refresh(c *gin.Context) {
	var body struct {
		Docroot       string `json:"docroot"`
		DB            string `json:"db"`
		OSUser        string `json:"os_user"`
		Domain        string `json:"domain"`
		SourceDocroot string `json:"source_docroot"`
		SourceSQL     string `json:"source_sql"`
		OldURL        string `json:"old_url"`
		NewURL        string `json:"new_url"`
		Force         bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if !body.Force {
		c.JSON(http.StatusBadRequest, gin.H{"error": "force_required", "detail": "refresh overwrites a live account; a pre-overwrite backup is taken automatically"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	c.Set("audit_target", body.Domain+" migration-refresh")
	c.Set("audit_target_type", "migration_refresh")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	res, err := migrate.RunRefresh(ctx, h.cfg.Agent, migrate.RefreshInput{
		Docroot: body.Docroot, DBName: body.DB, OSUser: body.OSUser, Domain: body.Domain,
		SrcDocroot: body.SourceDocroot, SrcSQL: body.SourceSQL, OldURL: body.OldURL, NewURL: body.NewURL,
	}, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "refresh_failed", "detail": err.Error(), "result": res})
		return
	}
	c.JSON(http.StatusOK, res)
}
