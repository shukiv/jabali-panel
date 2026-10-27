package api

import (
	"context"
	"encoding/json"
	"errors"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/cronops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// CronHandlerConfig bundles the dependencies of /api/v1/cron handlers.
type CronHandlerConfig struct {
	CronJobs repository.CronJobRepository
	Users    repository.UserRepository
	Domains  repository.DomainRepository
	Agent    agent.AgentInterface
	Log      *slog.Logger
	// Packages decides whether a job may run the account's shell scripts:
	// when its package includes SSH (GH #2290). Optional; nil keeps the
	// closed list.
	Packages repository.PackageRepository
}

// cronopsDeps adapts the handler config to the cronops seam.
func (h *cronHandler) cronopsDeps() cronops.Deps {
	return cronops.Deps{
		Users:    h.cfg.Users,
		Domains:  h.cfg.Domains,
		CronJobs: h.cfg.CronJobs,
		Agent:    h.cfg.Agent,
		Packages: h.cfg.Packages,
	}
}

// mapCronopsErr translates cronops sentinels to the existing HTTP
// contract (unchanged status codes / error bodies).
func (h *cronHandler) mapCronopsErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cronvalidate.ErrNoLinuxAccount), errors.Is(err, cronops.ErrUserNotFound):
		c.JSON(http.StatusConflict, gin.H{"error": "user_has_no_linux_account"})
	case errors.Is(err, cronops.ErrNameInvalid):
		respondValidationErr(c, "name", err)
	case errors.Is(err, cronops.ErrScheduleInvalid):
		respondValidationErr(c, "schedule", err)
	case errors.Is(err, cronops.ErrCommandInvalid):
		respondValidationErr(c, "command", err)
	case errors.Is(err, cronops.ErrJobNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	case errors.Is(err, cronops.ErrRootOwnerNotAdmin):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "root_cron_owner_not_admin",
			"detail": "a root cron job must be owned by an admin account; delete it and recreate it as an admin",
		})
	case errors.Is(err, cronops.ErrAgentFailed):
		respondAgentErr(c, "agent_apply_failed", err)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
	}
}

// RegisterCronRoutes mounts /cron CRUD + run-now + log under the given group
// (expected to be /api/v1). All routes require an authenticated caller.
func RegisterCronRoutes(g *gin.RouterGroup, cfg CronHandlerConfig) {
	h := &cronHandler{cfg: cfg}
	grp := g.Group("/cron")
	grp.GET("", h.list)
	grp.POST("", h.create)
	grp.GET("/:id", h.get)
	grp.PATCH("/:id", h.update)
	grp.DELETE("/:id", h.delete)
	grp.POST("/:id/run-now", h.runNow)
	grp.GET("/:id/log", h.readLog)

	// Admin view across all tenants. Other admin actions (PATCH /cron/:id,
	// DELETE /cron/:id, run-now, log) already accept any admin caller via
	// fetchAndAuthorize's `claims.IsAdmin` bypass — no separate /admin
	// variant needed for those.
	// GH #699: group-level RequireAdmin so any future /admin/cron route is
	// admin-gated by default (not just per-handler). adminListAll already checks
	// IsAdmin; this is the belt-and-suspenders guard.
	admin := g.Group("/admin/cron", middleware.RequireAdmin())
	admin.GET("", h.adminListAll)
}

type cronHandler struct{ cfg CronHandlerConfig }

// ---- request/response shapes ----

type createCronRequest struct {
	Name     string `json:"name" binding:"required"`
	Command  string `json:"command" binding:"required"`
	Schedule string `json:"schedule" binding:"required"`
	Enabled  *bool  `json:"enabled"`
	// UserID lets admins create cron jobs for any tenant. Tenants
	// supplying this field have it silently ignored -- the job
	// always lands under their own UserID. Empty / omitted means
	// "create for the caller" for both roles.
	UserID string `json:"user_id,omitempty"`
	// RunAsRoot is admin-only. The cron command runs as root (uid 0)
	// via a system-scoped systemd timer, not under the owner's
	// per-user systemd. Tenants supplying this field have it
	// silently dropped. A root job is owned by the admin who creates
	// it: an admin combining it with another account's UserID gets 422.
	RunAsRoot bool `json:"run_as_root,omitempty"`
}

type updateCronRequest struct {
	Name     *string `json:"name"`
	Command  *string `json:"command"`
	Schedule *string `json:"schedule"`
	Enabled  *bool   `json:"enabled"`
}

// cronJobResponse carries run_as_root so the admin editor can tell a root job
// from a tenant job (GH #1686 item 2): a root job's user_id is the creating
// admin's, so the owner alone can't distinguish them. It is read-only — PATCH
// never changes it.
type cronJobResponse struct {
	ID           string     `json:"id"`
	UserID       string     `json:"user_id"`
	Name         string     `json:"name"`
	Command      string     `json:"command"`
	Schedule     string     `json:"schedule"`
	Enabled      bool       `json:"enabled"`
	RunAsRoot    bool       `json:"run_as_root"`
	LastRunAt    *time.Time `json:"last_run_at"`
	LastExitCode *int       `json:"last_exit_code"`
	LastError    *string    `json:"last_error"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type runNowResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Output is the combined stdout+stderr of a run that went through the
	// job's unit (the journal keeps one stream); Stdout/Stderr stay empty.
	Output string `json:"output,omitempty"`
	// Running: the run outlived the wait and carries on in the background
	// (GH #2122); its output goes to the job's log.
	Running        bool `json:"running,omitempty"`
	AlreadyRunning bool `json:"already_running,omitempty"`
}

type cronLogResponse struct {
	Log   string `json:"log"`
	Lines int    `json:"lines"`
}

type cronRemoveAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	JobID     string `json:"job_id"`
	RunAsRoot bool   `json:"run_as_root,omitempty"`
}

type cronRunNowAgentParams struct {
	UserID        string   `json:"user_id"`
	Username      string   `json:"username"`
	JobID         string   `json:"job_id"`
	Command       string   `json:"command"`
	OwnedDocroots []string `json:"owned_docroots"`
	OwnedDomains  []string `json:"owned_domains"`
	WaitSeconds   int      `json:"wait_seconds,omitempty"`
	RunAsRoot     bool     `json:"run_as_root,omitempty"`
	// AllowShellScripts: the account's package includes SSH (GH #2290).
	AllowShellScripts bool `json:"allow_shell_scripts,omitempty"`
}

type cronTailLogAgentParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	JobID    string `json:"job_id"`
	Lines    int    `json:"lines,omitempty"`
	// RunAsRoot: a root job's log is in the system journal (JAB-293).
	RunAsRoot bool `json:"run_as_root,omitempty"`
}

// ---- helpers ----

func toCronResponse(j *models.CronJob) cronJobResponse {
	return cronJobResponse{
		ID:           j.ID,
		UserID:       j.UserID,
		Name:         j.Name,
		Command:      j.Command,
		Schedule:     j.Schedule,
		Enabled:      j.Enabled,
		RunAsRoot:    j.RunAsRoot,
		LastRunAt:    j.LastRunAt,
		LastExitCode: j.LastExitCode,
		LastError:    j.LastError,
		CreatedAt:    j.CreatedAt,
		UpdatedAt:    j.UpdatedAt,
	}
}

func (h *cronHandler) linuxUsername(ctx context.Context, userID string) (string, error) {
	u, err := h.cfg.Users.FindByID(ctx, userID)
	if err != nil {
		return "", err
	}
	uname := ""
	if u != nil && u.Username != nil {
		uname = *u.Username
	}
	if err := cronvalidate.ValidateLinuxAccount(uname); err != nil {
		return "", err
	}
	return uname, nil
}

// jobAccount names the account Run now and the log act as for a job. A root
// job (JAB-293) is root, reached through its system unit — but only while
// its owner is an admin: a tenant-owned root row (from before cronops held
// that invariant) would otherwise have root run a command the tenant can
// edit, so it is refused here as cronops refuses to edit it. ok is false
// once the response has been written.
func (h *cronHandler) jobAccount(ctx context.Context, c *gin.Context, job *models.CronJob) (username string, runAsRoot, ok bool) {
	if job.RunAsRoot {
		owner, err := h.cfg.Users.FindByID(ctx, job.UserID)
		if err != nil || owner == nil || !owner.IsAdmin {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "root_cron_owner_not_admin",
				"detail": "a root cron job must be owned by an admin account; delete it and recreate it as an admin",
			})
			return "", false, false
		}
		return "root", true, true
	}
	username, err := h.linuxUsername(ctx, job.UserID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "user_has_no_linux_account"})
		return "", false, false
	}
	return username, false, true
}

func (h *cronHandler) fetchAndAuthorize(ctx context.Context, c *gin.Context, id string) (*models.CronJob, bool) {
	job, err := h.cfg.CronJobs.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return nil, false
	}
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	if !claims.IsAdmin && job.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, false
	}
	return job, true
}

// respondValidationErr translates a cronvalidate error into a 400 response,
// preserving the structured Code so the UI can surface per-field messages.
func respondValidationErr(c *gin.Context, field string, err error) {
	var ve *cronvalidate.ValidationError
	if errors.As(err, &ve) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "validation_failed",
			"field":  field,
			"code":   ve.Code,
			"detail": ve.Detail,
		})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"error":  "validation_failed",
		"field":  field,
		"detail": err.Error(),
	})
}

// ---- handlers ----

func (h *cronHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	jobs, err := h.cfg.CronJobs.ListByUserID(ctx, claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	out := make([]cronJobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toCronResponse(j))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// adminCronJobResponse is the admin list view — same as cronJobResponse
// plus the owner's username so the UI can render "owner" column without
// a second N+1 lookup. user_id stays in the payload too in case the UI
// wants to filter / link.
type adminCronJobResponse struct {
	cronJobResponse
	Username string `json:"username"`
}

// adminListAll returns every cron job on the system, with the owner's
// username denormalised. Admin-only. Linear bulk-load of users — no
// N+1 — because the cron table is small enough to enumerate in one
// query and the user table is typically <1000 rows.
func (h *cronHandler) adminListAll(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	jobs, err := h.cfg.CronJobs.ListAll(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}

	// Bulk-load usernames so the response carries the owner without
	// extra round-trips client-side. Dedupe ids first; small N either
	// way but keeps the query plan tight.
	seen := make(map[string]struct{}, len(jobs))
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		if _, dup := seen[j.UserID]; dup {
			continue
		}
		seen[j.UserID] = struct{}{}
		ids = append(ids, j.UserID)
	}
	usernameByID := make(map[string]string, len(ids))
	if len(ids) > 0 && h.cfg.Users != nil {
		users, uerr := h.cfg.Users.FindByIDs(ctx, ids)
		if uerr == nil {
			for _, u := range users {
				if u.Username != nil {
					usernameByID[u.ID] = *u.Username
				}
			}
		}
	}

	out := make([]adminCronJobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, adminCronJobResponse{
			cronJobResponse: toCronResponse(j),
			Username:        usernameByID[j.UserID],
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *cronHandler) create(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req createCronRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// Thin adapter over cronops (the Cron Job Intake — ADR-0083/0101).
	owner := claims.UserID
	if claims.IsAdmin && req.UserID != "" {
		owner = req.UserID
	}
	runAsRoot := false
	if claims.IsAdmin {
		runAsRoot = req.RunAsRoot
	}
	// A root job is owned by the admin who creates it. Its command is checked
	// against the owner's docroots, so root + a tenant's user_id would have
	// root run a script that tenant can edit. Refuse the combination loudly
	// (cronops also enforces an admin owner for any door).
	if runAsRoot && owner != claims.UserID {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "run_as_root_owner_mismatch",
			"detail": "run_as_root cannot be combined with another account's user_id; a root cron job is owned by the admin who creates it",
		})
		return
	}
	job, err := cronops.Create(ctx, h.cronopsDeps(), cronops.CreateInput{
		UserID:    owner,
		RunAsRoot: runAsRoot,
		Name:      req.Name,
		Command:   req.Command,
		Schedule:  req.Schedule,
		Enabled:   enabled,
	})
	if err != nil {
		h.mapCronopsErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toCronResponse(job))
}

func (h *cronHandler) get(c *gin.Context) {
	job, ok := h.fetchAndAuthorize(c.Request.Context(), c, c.Param("id"))
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toCronResponse(job))
}

func (h *cronHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	// Authorization stays in the adapter (ownership / claims);
	// cronops owns only the intake invariant (ADR-0101).
	job, ok := h.fetchAndAuthorize(ctx, c, c.Param("id"))
	if !ok {
		return
	}
	var req updateCronRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	updated, err := cronops.Update(ctx, h.cronopsDeps(), job.ID, cronops.UpdatePatch{
		Name:     req.Name,
		Command:  req.Command,
		Schedule: req.Schedule,
		Enabled:  req.Enabled,
	})
	if err != nil {
		h.mapCronopsErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toCronResponse(updated))
}

func (h *cronHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()
	job, ok := h.fetchAndAuthorize(ctx, c, c.Param("id"))
	if !ok {
		return
	}
	// JAB-293: route through the shared cronops.Delete so REST + CLI share one
	// safe path — synchronous host timer removal, row dropped only on success,
	// row KEPT for retry on agent failure (a last-job orphan is otherwise never
	// swept by the reconciler).
	if err := cronops.Delete(ctx, h.cronopsDeps(), job.ID); err != nil {
		h.mapCronopsErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *cronHandler) runNow(c *gin.Context) {
	ctx := c.Request.Context()
	job, ok := h.fetchAndAuthorize(ctx, c, c.Param("id"))
	if !ok {
		return
	}
	if !job.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "job_disabled"})
		return
	}
	username, runAsRoot, ok := h.jobAccount(ctx, c, job)
	if !ok {
		return
	}

	// Resolve the user's owned docroots so the agent can re-validate the
	// command (defense-in-depth) before executing it.
	var docroots, domains []string
	if h.cfg.Domains != nil {
		if owned, _, dErr := h.cfg.Domains.ListByUserID(ctx, job.UserID, repository.ListOptions{Limit: 1000}); dErr == nil {
			for _, dm := range owned {
				if dm.DocRoot != "" {
					docroots = append(docroots, dm.DocRoot)
				}
				if dm.Name != "" {
					domains = append(domains, dm.Name)
				}
			}
		}
	}
	// GH #2290: a job that runs the account's shell script needs SSH in its
	// package; without it, say so here instead of the agent's refusal.
	pol := cronops.ShellPolicy(ctx, h.cfg.Users, h.cfg.Packages, job.UserID, runAsRoot)
	if !pol.ShellScripts && cronvalidate.UsesShell(job.Command) {
		respondValidationErr(c, "command", &cronvalidate.ValidationError{
			Code:   cronvalidate.ErrCodeShellNeedsSSH,
			Detail: "shell scripts need SSH access in your hosting plan",
		})
		return
	}
	// GH #2122: the agent waits cronRunNowWaitSeconds for the run, then answers
	// that it is still running and leaves it running. Bounding the call here
	// keeps that answer inside the HTTP server's 30 s write timeout.
	callCtx, cancel := context.WithTimeout(ctx, cronRunNowCallTimeout)
	defer cancel()
	result, err := h.cfg.Agent.Call(callCtx, "cron.run_now", cronRunNowAgentParams{
		UserID: job.UserID, Username: username, JobID: job.ID,
		Command: job.Command, OwnedDocroots: docroots, OwnedDomains: domains,
		WaitSeconds: cronRunNowWaitSeconds, RunAsRoot: runAsRoot,
		AllowShellScripts: pol.ShellScripts,
	})
	if err != nil {
		respondAgentErr(c, "agent_run_now_failed", err)
		return
	}
	var resp runNowResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}
	if resp.AlreadyRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "already_running"})
		return
	}
	if resp.Running {
		// Last run is NOT written here: the reconciler's cron.status harvest
		// records the run when it ends, and it only writes a newer timestamp,
		// so a "now, exit 0" written for an unfinished run would stick.
		c.JSON(http.StatusAccepted, resp)
		return
	}
	// Persist the run outcome so the UI's "Last Run" / "Last Exit" columns
	// reflect a manual Run-now (previously nothing ever called UpdateStatus,
	// so Last Run stayed "Never" even after a successful run). Best-effort.
	if h.cfg.CronJobs != nil {
		lastErr := ""
		if resp.ExitCode != 0 {
			lastErr = resp.Stderr
			if lastErr == "" {
				lastErr = resp.Output
			}
			lastErr = cronLastErrorTail(lastErr)
		}
		if uErr := h.cfg.CronJobs.UpdateStatus(ctx, job.ID, time.Now().UTC(), resp.ExitCode, lastErr); uErr != nil {
			h.cfg.Log.Warn("cron run_now: persist last-run failed", "job_id", job.ID, "err", uErr)
		}
	}
	c.JSON(http.StatusOK, resp)
}

// cronRunNowWaitSeconds is how long Run now waits for a run before answering
// that it is still running (GH #2122); cronRunNowCallTimeout bounds the whole
// agent call. Both stay under the HTTP server's 30 s write timeout.
const (
	cronRunNowWaitSeconds = 20
	cronRunNowCallTimeout = 27 * time.Second
)

// cronLastErrorMax is cron_jobs.last_error's VARCHAR(1024), in bytes so a
// multi-byte character can't push it over.
const cronLastErrorMax = 1024

// cronLastErrorTail keeps the end of a failed run's output — where the error
// usually is — within last_error's size, cut on a character boundary.
func cronLastErrorTail(s string) string {
	if len(s) <= cronLastErrorMax {
		return s
	}
	s = s[len(s)-cronLastErrorMax:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

func (h *cronHandler) readLog(c *gin.Context) {
	ctx := c.Request.Context()
	job, ok := h.fetchAndAuthorize(ctx, c, c.Param("id"))
	if !ok {
		return
	}
	username, runAsRoot, ok := h.jobAccount(ctx, c, job)
	if !ok {
		return
	}

	lines := 50
	if q := c.Query("lines"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			lines = n
			if lines > 500 {
				lines = 500
			}
		}
	}

	result, err := h.cfg.Agent.Call(ctx, "cron.tail_log", cronTailLogAgentParams{
		UserID: job.UserID, Username: username, JobID: job.ID, Lines: lines,
		RunAsRoot: runAsRoot,
	})
	if err != nil {
		respondAgentErr(c, "agent_tail_log_failed", err)
		return
	}
	var resp cronLogResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}
	if resp.Lines == 0 {
		resp.Lines = lines
	}
	c.JSON(http.StatusOK, resp)
}

// ---- agent dispatch helpers ----
