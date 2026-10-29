package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// BotChallengeExclusionRepository stores the paths of a domain that skip the
// AppSec bot-detection challenge (GH #2328).
type BotChallengeExclusionRepository interface {
	// List returns every row, for the reconciler.
	List(ctx context.Context) ([]models.BotChallengeExclusion, error)
	ListByDomain(ctx context.Context, domainID string) ([]models.BotChallengeExclusion, error)
	CountByDomain(ctx context.Context, domainID string) (int64, error)
	// Create returns ErrConflict when the domain already has the same path
	// and methods.
	Create(ctx context.Context, e *models.BotChallengeExclusion) error
	// DeleteForDomain removes one row of one domain. ErrNotFound when no row
	// with that id belongs to the domain.
	DeleteForDomain(ctx context.Context, domainID, id string) error
}

type botChallengeExclusionRepo struct{ db *gorm.DB }

func NewBotChallengeExclusionRepository(db *gorm.DB) BotChallengeExclusionRepository {
	return &botChallengeExclusionRepo{db: db}
}

func (r *botChallengeExclusionRepo) List(ctx context.Context) ([]models.BotChallengeExclusion, error) {
	var out []models.BotChallengeExclusion
	err := r.db.WithContext(ctx).
		Order("domain_id asc, path_prefix asc, methods asc").
		Find(&out).Error
	return out, err
}

func (r *botChallengeExclusionRepo) ListByDomain(ctx context.Context, domainID string) ([]models.BotChallengeExclusion, error) {
	var out []models.BotChallengeExclusion
	err := r.db.WithContext(ctx).
		Where("domain_id = ?", domainID).
		Order("path_prefix asc, methods asc").
		Find(&out).Error
	return out, err
}

func (r *botChallengeExclusionRepo) CountByDomain(ctx context.Context, domainID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&models.BotChallengeExclusion{}).
		Where("domain_id = ?", domainID).
		Count(&n).Error
	return n, err
}

func (r *botChallengeExclusionRepo) Create(ctx context.Context, e *models.BotChallengeExclusion) error {
	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(e).Error; err != nil {
		if isDuplicateKey(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (r *botChallengeExclusionRepo) DeleteForDomain(ctx context.Context, domainID, id string) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND domain_id = ?", id, domainID).
		Delete(&models.BotChallengeExclusion{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
