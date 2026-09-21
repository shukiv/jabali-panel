package repository

import (
	"context"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CRSRuleExclusionRepository stores operator-managed CRS exclusions (JAB-227).
type CRSRuleExclusionRepository interface {
	List(ctx context.Context) ([]models.CRSRuleExclusion, error)
	Create(ctx context.Context, e *models.CRSRuleExclusion) error
	DeleteByID(ctx context.Context, id string) error
}

type crsRuleExclusionRepo struct{ db *gorm.DB }

func NewCRSRuleExclusionRepository(db *gorm.DB) CRSRuleExclusionRepository {
	return &crsRuleExclusionRepo{db: db}
}

// List returns every exclusion, ordered so the rendered plugin is stable.
func (r *crsRuleExclusionRepo) List(ctx context.Context) ([]models.CRSRuleExclusion, error) {
	var out []models.CRSRuleExclusion
	err := r.db.WithContext(ctx).
		Order("host asc, uri_prefix asc, rule_id asc").
		Find(&out).Error
	return out, err
}

func (r *crsRuleExclusionRepo) Create(ctx context.Context, e *models.CRSRuleExclusion) error {
	return r.db.WithContext(ctx).Create(e).Error
}

// DeleteByID removes an exclusion by id. It returns ErrNotFound when no row
// matched (GORM Delete reports nil for a zero-row delete), so `appsec exclusion
// rm` cannot print "removed" — and fire a false-success audit row — for an id
// that was never there. Mirrors the host-mode clear guard (GH #1641).
func (r *crsRuleExclusionRepo) DeleteByID(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&models.CRSRuleExclusion{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
