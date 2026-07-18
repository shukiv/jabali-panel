package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CacheWarmupRunRepository persists cache-warmup run records (JAB-95 Phase 3).
type CacheWarmupRunRepository interface {
	Create(ctx context.Context, run *models.CacheWarmupRun) error
	Update(ctx context.Context, run *models.CacheWarmupRun) error
	LatestForInstall(ctx context.Context, installID string) (*models.CacheWarmupRun, error)
}

type cacheWarmupRunRepo struct{ db *gorm.DB }

func NewCacheWarmupRunRepository(db *gorm.DB) CacheWarmupRunRepository {
	return &cacheWarmupRunRepo{db: db}
}

func (r *cacheWarmupRunRepo) Create(ctx context.Context, run *models.CacheWarmupRun) error {
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *cacheWarmupRunRepo) Update(ctx context.Context, run *models.CacheWarmupRun) error {
	return r.db.WithContext(ctx).Save(run).Error
}

// LatestForInstall returns the most recent run for an install, or ErrNotFound.
func (r *cacheWarmupRunRepo) LatestForInstall(ctx context.Context, installID string) (*models.CacheWarmupRun, error) {
	var run models.CacheWarmupRun
	err := r.db.WithContext(ctx).
		Where("install_id = ?", installID).
		Order("created_at DESC").
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}
