package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/systemjobs"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// systemJobsScheduleLister is the one backup-schedule read the System jobs
// list needs.
type systemJobsScheduleLister interface {
	List(ctx context.Context) ([]models.BackupSchedule, error)
}

// AdminSystemJobsHandlerConfig holds the dependencies of the admin System
// jobs list (GH #1686). Schedules is optional: without it the list has no
// backup-schedule rows. RunRateLimit is optional (wire rl.StrictPerActor()).
type AdminSystemJobsHandlerConfig struct {
	Agent        agent.AgentInterface
	Schedules    systemJobsScheduleLister
	RunRateLimit gin.HandlerFunc
	Log          *slog.Logger
}

// RegisterAdminSystemJobsRoutes mounts the admin System jobs endpoints:
//
//	GET  /admin/system-jobs           every scheduled job Jabali installed, plus server-wide backup schedules
//	POST /admin/system-jobs/:id/run   start a job now (only where the catalog allows it)
//	GET  /admin/system-jobs/:id/log   the job's recent journal
//
// Jobs come from the fixed catalog in internal/systemjobs; a request names a
// catalog id and never a unit. The agent enforces the same catalog.
func RegisterAdminSystemJobsRoutes(g *gin.RouterGroup, cfg AdminSystemJobsHandlerConfig) {
	if cfg.Agent == nil {
		return
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &adminSystemJobsHandler{cfg: cfg}
	grp := g.Group("/admin/system-jobs")
	grp.Use(middleware.RequireAdmin())
	grp.GET("", h.list)
	if cfg.RunRateLimit != nil {
		grp.POST("/:id/run", cfg.RunRateLimit, h.run)
	} else {
		grp.POST("/:id/run", h.run)
	}
	grp.GET("/:id/log", h.log)
}

type adminSystemJobsHandler struct{ cfg AdminSystemJobsHandlerConfig }

// systemJobIDRe bounds the path parameter before the catalog lookup.
var systemJobIDRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

const (
	systemJobKindTimer          = "timer"
	systemJobKindBackupSchedule = "backup_schedule"
	systemJobManagedByBackups   = "backups"
)

// systemJobRow is one row of the admin list.
type systemJobRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Category    string `json:"category"`
	// Schedule is short text ("Daily at 04:30 UTC"); for a backup schedule
	// with ScheduleFormat "cron" it is the cron expression.
	Schedule       string  `json:"schedule"`
	ScheduleFormat string  `json:"schedule_format"` // "text" | "cron"
	Status         string  `json:"status"`          // running | scheduled | disabled
	LastResult     string  `json:"last_result"`     // success | failed | never | running | "" (not tracked here)
	LastRunAt      *string `json:"last_run_at"`
	NextRunAt      *string `json:"next_run_at"`
	CanRunNow      bool    `json:"can_run_now"`
	HasLog         bool    `json:"has_log"`
	ManagedBy      string  `json:"managed_by,omitempty"` // updates | backups
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func (h *adminSystemJobsHandler) list(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.jobs_list", nil)
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var resp systemjobs.ListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}

	rows := make([]systemJobRow, 0, len(resp.Jobs)+4)
	for _, st := range resp.Jobs {
		job, ok := systemjobs.Lookup(st.ID)
		if !ok {
			continue // an agent from a newer build; panel only shows what it knows
		}
		last := st.LastStartedAt
		if st.LastFinishedAt != "" && st.Status() != systemjobs.StatusRunning {
			last = st.LastFinishedAt
		}
		rows = append(rows, systemJobRow{
			ID:             job.ID,
			Kind:           systemJobKindTimer,
			Label:          job.Label,
			Description:    job.Description,
			Category:       string(job.Category),
			Schedule:       systemjobs.DescribeSchedule(st.Calendar, st.Every),
			ScheduleFormat: "text",
			Status:         st.Status(),
			LastResult:     st.LastResult(),
			LastRunAt:      optString(last),
			NextRunAt:      optString(st.NextRunAt),
			CanRunNow:      job.RunNow,
			HasLog:         true,
			ManagedBy:      job.ManagedBy,
		})
	}
	rows = append(rows, h.backupScheduleRows(ctx)...)

	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows), "page": 1, "page_size": len(rows)})
}

var backupCadenceText = map[string]string{
	"hourly":    "Every hour",
	"every_6h":  "Every 6 hours",
	"every_12h": "Every 12 hours",
	"daily":     "Daily",
	"weekly":    "Weekly",
}

// backupScheduleRows lists the server-wide backup schedules (the ones with no
// owning tenant) as rows that link to the Backups page, where they are
// managed and where their run history lives. Tenants' own schedules are
// tenant jobs and stay out of this list. Best effort: a read failure only
// drops these rows.
func (h *adminSystemJobsHandler) backupScheduleRows(ctx context.Context) []systemJobRow {
	if h.cfg.Schedules == nil {
		return nil
	}
	schedules, err := h.cfg.Schedules.List(ctx)
	if err != nil {
		h.cfg.Log.Warn("system jobs: list backup schedules", "err", err)
		return nil
	}
	var rows []systemJobRow
	for _, s := range schedules {
		if s.UserID != nil {
			continue
		}
		label := "Account backups"
		switch {
		case s.Kind == models.BackupScheduleKindSystem:
			label = "System backup"
		case s.IsManagedDefault:
			label = "Default local backups"
		case s.IncludeSystemBackup:
			label = "Account and system backups"
		}
		schedule, format := s.CronExpr, "cron"
		if text, ok := backupCadenceText[s.Cadence]; ok {
			schedule, format = text, "text"
		}
		status := systemjobs.StatusDisabled
		next := (*string)(nil)
		if s.Enabled {
			status = systemjobs.StatusScheduled
			next = optTime(s.NextRunAt)
		}
		rows = append(rows, systemJobRow{
			ID:             "backup-schedule-" + s.ID,
			Kind:           systemJobKindBackupSchedule,
			Label:          label,
			Description:    "A backup schedule. Change it, run it and see its results on the Backups page.",
			Category:       string(systemjobs.CategoryBackups),
			Schedule:       schedule,
			ScheduleFormat: format,
			Status:         status,
			LastRunAt:      optTime(s.LastRunAt),
			NextRunAt:      next,
			ManagedBy:      systemJobManagedByBackups,
		})
	}
	return rows
}

// lookupJob validates the :id path parameter and resolves it in the catalog.
func (h *adminSystemJobsHandler) lookupJob(c *gin.Context) (systemjobs.Job, bool) {
	id := c.Param("id")
	if !systemJobIDRe.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_system_job_id"})
		return systemjobs.Job{}, false
	}
	job, ok := systemjobs.Lookup(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown_system_job"})
		return systemjobs.Job{}, false
	}
	return job, true
}

// agentNotFound reports whether the agent answered not_found (the job's unit
// is not installed on this server).
func agentNotFound(err error) bool {
	var ae *agent.AgentError
	return errors.As(err, &ae) && ae.Code == agent.CodeNotFound
}

func (h *adminSystemJobsHandler) run(c *gin.Context) {
	job, ok := h.lookupJob(c)
	if !ok {
		return
	}
	if !job.RunNow {
		resp := gin.H{"error": "run_now_not_allowed"}
		if job.ManagedBy == systemjobs.ManagedByUpdates {
			resp["details"] = "this job is run from the Updates page"
		}
		c.JSON(http.StatusUnprocessableEntity, resp)
		return
	}
	actorID := ""
	if claims := ginctx.Claims(c); claims != nil {
		actorID = claims.UserID
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.job_run", systemjobs.RunParams{ID: job.ID})
	if err != nil {
		h.cfg.Log.Warn("event=audit kind=system_job_run_failed", "actor_id", actorID, "job", job.ID, "err", err.Error())
		if agentNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "system_job_not_installed"})
			return
		}
		respondAgentErr(c, "agent_error", err)
		return
	}
	var resp systemjobs.RunResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}
	if resp.AlreadyRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "already_running"})
		return
	}
	h.cfg.Log.Info("event=audit kind=system_job_run", "actor_id", actorID, "job", job.ID)
	c.JSON(http.StatusAccepted, gin.H{"started": true})
}

func (h *adminSystemJobsHandler) log(c *gin.Context) {
	job, ok := h.lookupJob(c)
	if !ok {
		return
	}
	lines := systemjobs.DefaultLogLines
	if q := c.Query("lines"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			lines = systemjobs.ClampLogLines(n)
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.job_log", systemjobs.LogParams{ID: job.ID, Lines: lines})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var resp systemjobs.LogResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}
	c.JSON(http.StatusOK, resp)
}
