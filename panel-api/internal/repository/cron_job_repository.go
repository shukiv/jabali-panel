package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CronJobRepository defines data access for cron jobs.
type CronJobRepository interface {
	Create(ctx context.Context, job *models.CronJob) error
	FindByID(ctx context.Context, id string) (*models.CronJob, error)
	ListByUserID(ctx context.Context, userID string) ([]*models.CronJob, error)
	ListAll(ctx context.Context) ([]*models.CronJob, error)
	Update(ctx context.Context, job *models.CronJob) error
	UpdateStatus(ctx context.Context, id string, lastRunAt time.Time, exitCode int, lastError string) error
	Delete(ctx context.Context, id string) error
}

type cronJobRepo struct{ db *gorm.DB }

func NewCronJobRepository(db *gorm.DB) CronJobRepository {
	return &cronJobRepo{db: db}
}

func (r *cronJobRepo) Create(ctx context.Context, job *models.CronJob) error {
	// CronJob.Enabled intentionally carries NO gorm `default` tag: GORM
	// substitutes a defaulted field's default for a zero value (even
	// under Select), which silently flipped an intentional Enabled=false
	// to enabled=1 — defeating the cPanel cron importer's disabled-import
	// and any disabled cron via cronops/backup-restore. Without the tag,
	// the struct's Enabled is written verbatim.
	return translate(r.db.WithContext(ctx).Create(job).Error)
}

func (r *cronJobRepo) FindByID(ctx context.Context, id string) (*models.CronJob, error) {
	var job models.CronJob
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &job, nil
}

func (r *cronJobRepo) ListByUserID(ctx context.Context, userID string) ([]*models.CronJob, error) {
	var jobs []*models.CronJob
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *cronJobRepo) ListAll(ctx context.Context) ([]*models.CronJob, error) {
	var jobs []*models.CronJob
	if err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *cronJobRepo) Update(ctx context.Context, job *models.CronJob) error {
	if err := r.db.WithContext(ctx).Save(job).Error; err != nil {
		return err
	}
	return nil
}

func (r *cronJobRepo) UpdateStatus(ctx context.Context, id string, lastRunAt time.Time, exitCode int, lastError string) error {
	updates := map[string]interface{}{
		"last_run_at":    lastRunAt,
		"last_exit_code": exitCode,
		"last_error":     lastError,
	}
	if err := r.db.WithContext(ctx).Model(&models.CronJob{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}

func (r *cronJobRepo) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Delete(&models.CronJob{}, "id = ?", id)
	if err := result.Error; err != nil {
		return err
	}
	return nil
}
