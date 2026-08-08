// Package repository — BackupScheduleRepository owns backup_schedules
// + the M:N join with backup_destinations. M30.1 / ADR-0078.
//
// Cron parsing happens in the scheduler tick (internal/backup/cron.go);
// this layer treats CronExpr as opaque text. NextRunAt is set by the
// caller, not auto-computed here, so the scheduler's view of "now" is
// the only one that matters.
package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type BackupScheduleRepository interface {
	Create(ctx context.Context, s *models.BackupSchedule) error
	Get(ctx context.Context, id string) (*models.BackupSchedule, error)
	GetWithDestinations(ctx context.Context, id string) (*models.BackupSchedule, error)
	List(ctx context.Context) ([]models.BackupSchedule, error)
	ListForUser(ctx context.Context, userID string) ([]models.BackupSchedule, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]models.BackupSchedule, error)
	Update(ctx context.Context, s *models.BackupSchedule) error
	UpdateNextRun(ctx context.Context, id string, nextRunAt time.Time) error
	MarkRan(ctx context.Context, id string, ranAt, nextRunAt time.Time) error
	Delete(ctx context.Context, id string) error

	// Destination link helpers — replace performs a delete+insert in one
	// transaction so PATCH /admin/backup-schedules/:id with a new
	// destinations[] is atomic.
	GetDestinations(ctx context.Context, scheduleID string) ([]models.BackupDestination, error)
	ReplaceDestinations(ctx context.Context, scheduleID string, destIDs []string) error

	// User link helpers — multi-select per-schedule. Empty list = fan
	// out to every non-admin user at tick time; non-empty = those
	// specific users only.
	GetUserIDs(ctx context.Context, scheduleID string) ([]string, error)
	ReplaceUsers(ctx context.Context, scheduleID string, userIDs []string) error
}

type backupScheduleRepo struct{ db *gorm.DB }

func NewBackupScheduleRepository(db *gorm.DB) BackupScheduleRepository {
	return &backupScheduleRepo{db: db}
}

func (r *backupScheduleRepo) Create(ctx context.Context, s *models.BackupSchedule) error {
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = now
	}
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *backupScheduleRepo) Get(ctx context.Context, id string) (*models.BackupSchedule, error) {
	var out models.BackupSchedule
	if err := r.db.WithContext(ctx).First(&out, "id = ?", id).Error; err != nil {
		return nil, translate(err)
	}
	return &out, nil
}

func (r *backupScheduleRepo) GetWithDestinations(ctx context.Context, id string) (*models.BackupSchedule, error) {
	s, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	dests, err := r.GetDestinations(ctx, id)
	if err != nil {
		return nil, err
	}
	s.Destinations = dests
	return s, nil
}

func (r *backupScheduleRepo) List(ctx context.Context) ([]models.BackupSchedule, error) {
	var out []models.BackupSchedule
	if err := r.db.WithContext(ctx).
		Order("kind ASC, user_id ASC, cron_expr ASC").
		Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupScheduleRepo) ListForUser(ctx context.Context, userID string) ([]models.BackupSchedule, error) {
	var out []models.BackupSchedule
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("cron_expr ASC").
		Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupScheduleRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]models.BackupSchedule, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []models.BackupSchedule
	if err := r.db.WithContext(ctx).
		Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Order("next_run_at ASC").
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupScheduleRepo) Update(ctx context.Context, s *models.BackupSchedule) error {
	s.UpdatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&models.BackupSchedule{}).
		Where("id = ?", s.ID).
		Updates(map[string]any{
			"kind":                  s.Kind,
			"user_id":               s.UserID,
			"include_system_backup": s.IncludeSystemBackup,
			"cron_expr":             s.CronExpr,
			"content":               s.Content,
			"cadence":               s.Cadence,
			"enabled":               s.Enabled,
			"keep_daily":            s.KeepDaily,
			"keep_weekly":           s.KeepWeekly,
			"keep_monthly":          s.KeepMonthly,
			"next_run_at":           s.NextRunAt,
			"updated_at":            s.UpdatedAt,
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *backupScheduleRepo) UpdateNextRun(ctx context.Context, id string, nextRunAt time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&models.BackupSchedule{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"next_run_at": nextRunAt,
			"updated_at":  time.Now().UTC(),
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *backupScheduleRepo) MarkRan(ctx context.Context, id string, ranAt, nextRunAt time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&models.BackupSchedule{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_run_at": ranAt,
			"next_run_at": nextRunAt,
			"updated_at":  time.Now().UTC(),
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *backupScheduleRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.BackupSchedule{}, "id = ?", id)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *backupScheduleRepo) GetDestinations(ctx context.Context, scheduleID string) ([]models.BackupDestination, error) {
	var out []models.BackupDestination
	err := r.db.WithContext(ctx).
		Table("backup_destinations AS d").
		Joins("JOIN backup_schedule_destinations AS j ON j.destination_id = d.id").
		Where("j.schedule_id = ?", scheduleID).
		Order("d.name ASC").
		Find(&out).Error
	if err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupScheduleRepo) ReplaceDestinations(ctx context.Context, scheduleID string, destIDs []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("schedule_id = ?", scheduleID).
			Delete(&models.BackupScheduleDestination{}).Error; err != nil {
			return translate(err)
		}
		if len(destIDs) == 0 {
			return nil
		}
		now := time.Now().UTC()
		rows := make([]models.BackupScheduleDestination, 0, len(destIDs))
		for _, id := range destIDs {
			rows = append(rows, models.BackupScheduleDestination{
				ScheduleID:    scheduleID,
				DestinationID: id,
				CreatedAt:     now,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return translate(err)
		}
		return nil
	})
}

func (r *backupScheduleRepo) GetUserIDs(ctx context.Context, scheduleID string) ([]string, error) {
	var out []string
	err := r.db.WithContext(ctx).
		Model(&models.BackupScheduleUser{}).
		Where("schedule_id = ?", scheduleID).
		Order("user_id ASC").
		Pluck("user_id", &out).Error
	if err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupScheduleRepo) ReplaceUsers(ctx context.Context, scheduleID string, userIDs []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("schedule_id = ?", scheduleID).
			Delete(&models.BackupScheduleUser{}).Error; err != nil {
			return translate(err)
		}
		if len(userIDs) == 0 {
			return nil
		}
		now := time.Now().UTC()
		rows := make([]models.BackupScheduleUser, 0, len(userIDs))
		for _, id := range userIDs {
			rows = append(rows, models.BackupScheduleUser{
				ScheduleID: scheduleID,
				UserID:     id,
				CreatedAt:  now,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return translate(err)
		}
		return nil
	})
}
