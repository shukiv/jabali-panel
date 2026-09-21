package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CRSHostModeRepository stores operator-set per-host AppSec modes (GH #1641).
type CRSHostModeRepository interface {
	List(ctx context.Context) ([]models.CRSHostMode, error)
	// Upsert sets the mode for a host, replacing any existing row for that host
	// (host is unique — a host has at most one mode).
	Upsert(ctx context.Context, m *models.CRSHostMode) error
	DeleteByHost(ctx context.Context, host string) error
}

type crsHostModeRepo struct{ db *gorm.DB }

func NewCRSHostModeRepository(db *gorm.DB) CRSHostModeRepository {
	return &crsHostModeRepo{db: db}
}

// List returns every host mode, ordered so the rendered plugin is stable.
func (r *crsHostModeRepo) List(ctx context.Context) ([]models.CRSHostMode, error) {
	var out []models.CRSHostMode
	err := r.db.WithContext(ctx).Order("host asc").Find(&out).Error
	return out, err
}

// Upsert writes the row, updating mode/note/updated_at on the existing host
// rather than inserting a duplicate (uq_crs_host_mode). The ULID of a
// pre-existing row is preserved.
func (r *crsHostModeRepo) Upsert(ctx context.Context, m *models.CRSHostMode) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "host"}},
			DoUpdates: clause.AssignmentColumns([]string{"mode", "note", "updated_at"}),
		}).
		Create(m).Error
}

func (r *crsHostModeRepo) DeleteByHost(ctx context.Context, host string) error {
	return r.db.WithContext(ctx).
		Where("host = ?", host).
		Delete(&models.CRSHostMode{}).Error
}
