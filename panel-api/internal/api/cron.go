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
}

// cronopsDeps adapts the handler config to the cronops seam.
func (h *cronHandler) cronopsDeps() cronops.Deps {
	return cronops.Deps{
		Users:    h.cfg.Users,
		Domains:  h.cfg.Domains,
		CronJobs: h.cfg.CronJobs,
		Agent:    h.cfg.Agent,
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
	// silently dropped.
	RunAsRoot bool `json:"run_as_root,omitempty"`
}

type updateCronRequest struct {
	Name     *string `json:"name"`
	Command  *string `json:"command"`
	Schedule *string `json:"schedule"`
	Enabled  *bool   `json:"enabled"`
}

type cronJobResponse struct {
	ID           string     `json:"id"`
	UserID       string     `json:"user_id"`
	Name         string     `json:"name"`
	Command      string     `json:"command"`
	Schedule     string     `json:"schedule"`
	Enabled      bool       `json:"enabled"`
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
}

type cronTailLogAgentParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	JobID    string `json:"job_id"`
	Lines    int    `json:"lines,omitempty"`
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
	username, err := h.linuxUsername(ctx, job.UserID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "user_has_no_linux_account"})
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
	result, err := h.cfg.Agent.Call(ctx, "cron.run_now", cronRunNowAgentParams{
		UserID: job.UserID, Username: username, JobID: job.ID,
		Command: job.Command, OwnedDocroots: docroots, OwnedDomains: domains,
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
	// Persist the run outcome so the UI's "Last Run" / "Last Exit" columns
	// reflect a manual Run-now (previously nothing ever called UpdateStatus,
	// so Last Run stayed "Never" even after a successful run). Best-effort.
	if h.cfg.CronJobs != nil {
		lastErr := ""
		if resp.ExitCode != 0 {
			lastErr = resp.Stderr
		}
		if uErr := h.cfg.CronJobs.UpdateStatus(ctx, job.ID, time.Now().UTC(), resp.ExitCode, lastErr); uErr != nil {
			h.cfg.Log.Warn("cron run_now: persist last-run failed", "job_id", job.ID, "err", uErr)
		}
	}
	c.JSON(http.StatusOK, resp)
}

func (h *cronHandler) readLog(c *gin.Context) {
	ctx := c.Request.Context()
	job, ok := h.fetchAndAuthorize(ctx, c, c.Param("id"))
	if !ok {
		return
	}
	username, err := h.linuxUsername(ctx, job.UserID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "user_has_no_linux_account"})
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
