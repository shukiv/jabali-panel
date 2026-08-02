// M30.1 Step 8 — admin REST for backup_schedules + the
// backup_schedule_destinations join (ADR-0078).
//
// Cron expressions are validated server-side via internal/backup.ParseCron
// before insert. The handler returns the next 5 firings as a UI helper
// so the admin can sanity-check the schedule before saving.
package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type BackupSchedulesConfig struct {
	Schedules    repository.BackupScheduleRepository
	Destinations repository.BackupDestinationRepository
	Users        repository.UserRepository
	Jobs         repository.BackupJobRepository
}

func RegisterBackupScheduleRoutes(rg *gin.RouterGroup, cfg BackupSchedulesConfig) {
	if cfg.Schedules == nil || cfg.Destinations == nil {
		return
	}
	h := &backupScheduleHandler{cfg: cfg}
	admin := rg.Group("/admin", middleware.RequireAdmin())
	admin.GET("/backup-schedules", h.list)
	admin.GET("/backup-schedules/:id", h.get)
	admin.POST("/backup-schedules", h.create)
	admin.PATCH("/backup-schedules/:id", h.update)
	admin.DELETE("/backup-schedules/:id", h.delete)
	admin.POST("/backup-schedules/:id/run-now", h.runNow)
}

type backupScheduleHandler struct {
	cfg BackupSchedulesConfig
}

type scheduleDTO struct {
	ID                  string                 `json:"id"`
	Kind                string                 `json:"kind"`
	UserID              *string                `json:"user_id,omitempty"`
	UserIDs             []string               `json:"user_ids"`
	IncludeSystemBackup bool                   `json:"include_system_backup"`
	CronExpr            string                 `json:"cron_expr"`
	Enabled             bool                   `json:"enabled"`
	KeepDaily           *int                   `json:"keep_daily,omitempty"`
	KeepWeekly          *int                   `json:"keep_weekly,omitempty"`
	KeepMonthly         *int                   `json:"keep_monthly,omitempty"`
	LastRunAt           *string                `json:"last_run_at,omitempty"`
	NextRunAt           *string                `json:"next_run_at,omitempty"`
	NextFirings         []string               `json:"next_firings,omitempty"`
	Destinations        []backupDestinationDTO `json:"destinations,omitempty"`
}

func toScheduleDTO(s *models.BackupSchedule, dests []models.BackupDestination) scheduleDTO {
	dto := scheduleDTO{
		ID: s.ID, Kind: s.Kind, UserID: s.UserID,
		UserIDs:             append([]string{}, s.UserIDs...),
		IncludeSystemBackup: s.IncludeSystemBackup,
		CronExpr:            s.CronExpr, Enabled: s.Enabled,
		KeepDaily: s.KeepDaily, KeepWeekly: s.KeepWeekly, KeepMonthly: s.KeepMonthly,
	}
	if s.LastRunAt != nil {
		v := s.LastRunAt.Format(time.RFC3339)
		dto.LastRunAt = &v
	}
	if s.NextRunAt != nil {
		v := s.NextRunAt.Format(time.RFC3339)
		dto.NextRunAt = &v
	}
	if fires, err := internalbackup.PreviewFires(s.CronExpr, time.Now().UTC(), 5); err == nil {
		for _, f := range fires {
			dto.NextFirings = append(dto.NextFirings, f.Format(time.RFC3339))
		}
	}
	for i := range dests {
		dto.Destinations = append(dto.Destinations, toDestDTO(&dests[i]))
	}
	return dto
}

func (h *backupScheduleHandler) list(c *gin.Context) {
	rows, err := h.cfg.Schedules.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_list"})
		return
	}
	out := make([]scheduleDTO, 0, len(rows))
	for i := range rows {
		dests, _ := h.cfg.Schedules.GetDestinations(c.Request.Context(), rows[i].ID)
		uids, _ := h.cfg.Schedules.GetUserIDs(c.Request.Context(), rows[i].ID)
		rows[i].UserIDs = uids
		out = append(out, toScheduleDTO(&rows[i], dests))
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
}

func (h *backupScheduleHandler) get(c *gin.Context) {
	s, err := h.cfg.Schedules.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_get"})
		return
	}
	dests, _ := h.cfg.Schedules.GetDestinations(c.Request.Context(), s.ID)
	uids, _ := h.cfg.Schedules.GetUserIDs(c.Request.Context(), s.ID)
	s.UserIDs = uids
	c.JSON(http.StatusOK, toScheduleDTO(s, dests))
}

type createScheduleRequest struct {
	// Kind defaults to account_backup when omitted (UI no longer
	// exposes the system-only kind — system backups are an opt-in
	// flag on the account schedule).
	Kind string `json:"kind"`
	// Legacy single-user field; kept for backwards compat. New
	// callers should send UserIDs (multi-select). Empty UserIDs +
	// nil/empty UserID = "all non-admin users" fan-out at tick time.
	UserID              *string  `json:"user_id"`
	UserIDs             []string `json:"user_ids"`
	IncludeSystemBackup bool     `json:"include_system_backup"`
	CronExpr            string   `json:"cron_expr"       binding:"required"`
	Enabled             *bool    `json:"enabled"`
	KeepDaily           *int     `json:"keep_daily"`
	KeepWeekly          *int     `json:"keep_weekly"`
	KeepMonthly         *int     `json:"keep_monthly"`
	DestinationIDs      []string `json:"destination_ids"`
}

func (h *backupScheduleHandler) create(c *gin.Context) {
	var req createScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_body", "detail": err.Error()})
		return
	}
	// Kind defaults to account_backup so the UI doesn't have to send
	// it explicitly. system_backup kind is still accepted for direct
	// API callers (sysadmin tooling).
	if req.Kind == "" {
		req.Kind = models.BackupScheduleKindAccount
	}
	if req.Kind != models.BackupScheduleKindAccount && req.Kind != models.BackupScheduleKindSystem {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_kind"})
		return
	}
	// Normalise user_ids: drop empty strings, accept the legacy single
	// user_id by promoting it into UserIDs.
	if req.UserID != nil && *req.UserID != "" {
		req.UserIDs = append(req.UserIDs, *req.UserID)
	}
	req.UserID = nil
	cleanIDs := make([]string, 0, len(req.UserIDs))
	for _, uid := range req.UserIDs {
		if uid == "" {
			continue
		}
		cleanIDs = append(cleanIDs, uid)
	}
	req.UserIDs = cleanIDs
	if req.Kind == models.BackupScheduleKindAccount {
		for _, uid := range req.UserIDs {
			if h.cfg.Users == nil {
				break
			}
			user, err := h.cfg.Users.FindByID(c.Request.Context(), uid)
			if err != nil || user == nil {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "user_not_found", "detail": uid})
				return
			}
			if user.IsAdmin {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "admin_user_not_allowed", "detail": uid})
				return
			}
		}
	} else {
		req.UserIDs = nil
	}
	next, err := internalbackup.NextFire(req.CronExpr, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_cron", "detail": err.Error()})
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// include_system_backup is a no-op on kind=system_backup (those
	// schedules already back up the system); silently normalise to
	// false so the field isn't a confusing leftover after a kind flip.
	includeSys := req.IncludeSystemBackup
	if req.Kind == models.BackupScheduleKindSystem {
		includeSys = false
	}
	s := &models.BackupSchedule{
		ID:                  ids.NewULID(),
		Kind:                req.Kind,
		UserID:              req.UserID,
		IncludeSystemBackup: includeSys,
		CronExpr:            strings.TrimSpace(req.CronExpr),
		Enabled:             enabled,
		KeepDaily:           req.KeepDaily,
		KeepWeekly:          req.KeepWeekly,
		KeepMonthly:         req.KeepMonthly,
		NextRunAt:           &next,
	}
	if err := h.cfg.Schedules.Create(c.Request.Context(), s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_create"})
		return
	}
	if len(req.DestinationIDs) > 0 {
		if err := h.cfg.Schedules.ReplaceDestinations(c.Request.Context(), s.ID, req.DestinationIDs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_link_destinations"})
			return
		}
	}
	if err := h.cfg.Schedules.ReplaceUsers(c.Request.Context(), s.ID, req.UserIDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_link_users"})
		return
	}
	s.UserIDs = req.UserIDs
	dests, _ := h.cfg.Schedules.GetDestinations(c.Request.Context(), s.ID)
	c.JSON(http.StatusCreated, toScheduleDTO(s, dests))
}

type updateScheduleRequest struct {
	CronExpr            *string   `json:"cron_expr"`
	Enabled             *bool     `json:"enabled"`
	IncludeSystemBackup *bool     `json:"include_system_backup"`
	KeepDaily           *int      `json:"keep_daily"`
	KeepWeekly          *int      `json:"keep_weekly"`
	KeepMonthly         *int      `json:"keep_monthly"`
	DestinationIDs      *[]string `json:"destination_ids"`
	UserIDs             *[]string `json:"user_ids"`
}

func (h *backupScheduleHandler) update(c *gin.Context) {
	id := c.Param("id")
	s, err := h.cfg.Schedules.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_get"})
		return
	}
	var req updateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_body", "detail": err.Error()})
		return
	}
	if req.CronExpr != nil {
		next, err := internalbackup.NextFire(*req.CronExpr, time.Now().UTC())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_cron", "detail": err.Error()})
			return
		}
		s.CronExpr = strings.TrimSpace(*req.CronExpr)
		s.NextRunAt = &next
	}
	if req.Enabled != nil {
		s.Enabled = *req.Enabled
	}
	if req.IncludeSystemBackup != nil && s.Kind == models.BackupScheduleKindAccount {
		s.IncludeSystemBackup = *req.IncludeSystemBackup
	}
	if req.KeepDaily != nil {
		s.KeepDaily = req.KeepDaily
	}
	if req.KeepWeekly != nil {
		s.KeepWeekly = req.KeepWeekly
	}
	if req.KeepMonthly != nil {
		s.KeepMonthly = req.KeepMonthly
	}
	if err := h.cfg.Schedules.Update(c.Request.Context(), s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_update"})
		return
	}
	if req.DestinationIDs != nil {
		if err := h.cfg.Schedules.ReplaceDestinations(c.Request.Context(), s.ID, *req.DestinationIDs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_link_destinations"})
			return
		}
	}
	if req.UserIDs != nil && s.Kind == models.BackupScheduleKindAccount {
		clean := make([]string, 0, len(*req.UserIDs))
		for _, uid := range *req.UserIDs {
			if uid == "" {
				continue
			}
			if h.cfg.Users != nil {
				user, err := h.cfg.Users.FindByID(c.Request.Context(), uid)
				if err != nil || user == nil {
					c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "user_not_found", "detail": uid})
					return
				}
				if user.IsAdmin {
					c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "admin_user_not_allowed", "detail": uid})
					return
				}
			}
			clean = append(clean, uid)
		}
		if err := h.cfg.Schedules.ReplaceUsers(c.Request.Context(), s.ID, clean); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_link_users"})
			return
		}
		s.UserIDs = clean
	} else {
		ids, _ := h.cfg.Schedules.GetUserIDs(c.Request.Context(), s.ID)
		s.UserIDs = ids
	}
	dests, _ := h.cfg.Schedules.GetDestinations(c.Request.Context(), s.ID)
	c.JSON(http.StatusOK, toScheduleDTO(s, dests))
}

func (h *backupScheduleHandler) delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.cfg.Schedules.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_delete"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// runNow advances next_run_at to NOW so the scheduler tick fires the
// schedule on its next iteration. Doesn't bypass concurrency gates —
// if a backup is already running, the agent rejects the duplicate.
func (h *backupScheduleHandler) runNow(c *gin.Context) {
	id := c.Param("id")
	s, err := h.cfg.Schedules.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_get"})
		return
	}
	if !s.Enabled {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "schedule_disabled"})
		return
	}
	if err := h.cfg.Schedules.UpdateNextRun(c.Request.Context(), id, time.Now().UTC()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "db_update"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"status": "ok",
		"detail": "schedule next-run advanced; tick will fire within " + internalbackup.PresetCronExpr["daily"],
	})
}
