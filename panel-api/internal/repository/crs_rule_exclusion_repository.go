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

func (r *crsRuleExclusionRepo) DeleteByID(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&models.CRSRuleExclusion{}).Error
}
