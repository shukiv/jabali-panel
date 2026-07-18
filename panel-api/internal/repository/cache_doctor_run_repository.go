package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CacheDoctorRunRepository persists fleet cache-maintenance run records (JAB-11).
type CacheDoctorRunRepository interface {
	Create(ctx context.Context, run *models.CacheDoctorRun) error
	Update(ctx context.Context, run *models.CacheDoctorRun) error
	Get(ctx context.Context, id string) (*models.CacheDoctorRun, error)
	ListRecent(ctx context.Context, limit int) ([]models.CacheDoctorRun, error)
	// CountActive returns how many runs are queued or running — used to reject a
	// second concurrent sweep.
	CountActive(ctx context.Context) (int64, error)
}

type cacheDoctorRunRepo struct{ db *gorm.DB }

func NewCacheDoctorRunRepository(db *gorm.DB) CacheDoctorRunRepository {
	return &cacheDoctorRunRepo{db: db}
}

func (r *cacheDoctorRunRepo) Create(ctx context.Context, run *models.CacheDoctorRun) error {
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *cacheDoctorRunRepo) Update(ctx context.Context, run *models.CacheDoctorRun) error {
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *cacheDoctorRunRepo) Get(ctx context.Context, id string) (*models.CacheDoctorRun, error) {
	var run models.CacheDoctorRun
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *cacheDoctorRunRepo) ListRecent(ctx context.Context, limit int) ([]models.CacheDoctorRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var runs []models.CacheDoctorRun
	err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).
		Find(&runs).Error
	return runs, err
}

func (r *cacheDoctorRunRepo) CountActive(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&models.CacheDoctorRun{}).
		Where("state IN ?", []string{models.CacheDoctorStateQueued, models.CacheDoctorStateRunning}).
		Count(&n).Error
	return n, err
}
