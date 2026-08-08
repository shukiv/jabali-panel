package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DiskUsageSnapshotRepository persists the per-user disk-usage snapshot the
// tenant Disk Usage page reads (recomputed only on demand).
type DiskUsageSnapshotRepository interface {
	// Get returns the user's snapshot, or ErrNotFound if none exists yet.
	Get(ctx context.Context, userID string) (*models.DiskUsageSnapshot, error)
	// Upsert writes (or replaces) the user's snapshot.
	Upsert(ctx context.Context, userID, payload string, computedAt time.Time) error
	// ListAll returns every stored snapshot — one batch read for the
	// automation bulk-usage endpoint (ADR-0164; never per-user N+1).
	ListAll(ctx context.Context) ([]models.DiskUsageSnapshot, error)
}

type diskUsageSnapshotRepo struct{ db *gorm.DB }

func NewDiskUsageSnapshotRepository(db *gorm.DB) DiskUsageSnapshotRepository {
	return &diskUsageSnapshotRepo{db: db}
}

func (r *diskUsageSnapshotRepo) Get(ctx context.Context, userID string) (*models.DiskUsageSnapshot, error) {
	var s models.DiskUsageSnapshot
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *diskUsageSnapshotRepo) Upsert(ctx context.Context, userID, payload string, computedAt time.Time) error {
	now := time.Now()
	s := models.DiskUsageSnapshot{
		UserID:     userID,
		Payload:    payload,
		ComputedAt: computedAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"payload", "computed_at", "updated_at"}),
	}).Create(&s).Error
}

func (r *diskUsageSnapshotRepo) ListAll(ctx context.Context) ([]models.DiskUsageSnapshot, error) {
	var rows []models.DiskUsageSnapshot
	if err := r.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
