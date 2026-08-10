package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainTeardownRepository persists the JAB-236 teardown tombstones. See
// models.DomainTeardown for the lifecycle.
type DomainTeardownRepository interface {
	// Ensure creates the tombstone if absent — idempotent by primary key,
	// so racing delete paths cannot duplicate it.
	Ensure(ctx context.Context, domainName string) error
	List(ctx context.Context) ([]models.DomainTeardown, error)
	Delete(ctx context.Context, domainName string) error
	// MarkAttempt bumps the attempt counter and records the failure.
	MarkAttempt(ctx context.Context, domainName, lastError string) error
}

type domainTeardownRepo struct{ db *gorm.DB }

func NewDomainTeardownRepository(db *gorm.DB) DomainTeardownRepository {
	return &domainTeardownRepo{db: db}
}

func (r *domainTeardownRepo) Ensure(ctx context.Context, domainName string) error {
	row := &models.DomainTeardown{DomainName: domainName, CreatedAt: time.Now().UTC()}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func (r *domainTeardownRepo) List(ctx context.Context) ([]models.DomainTeardown, error) {
	var rows []models.DomainTeardown
	err := r.db.WithContext(ctx).Order("created_at").Find(&rows).Error
	return rows, err
}

func (r *domainTeardownRepo) Delete(ctx context.Context, domainName string) error {
	return r.db.WithContext(ctx).Where("domain_name = ?", domainName).
		Delete(&models.DomainTeardown{}).Error
}

func (r *domainTeardownRepo) MarkAttempt(ctx context.Context, domainName, lastError string) error {
	if len(lastError) > 1024 {
		lastError = lastError[:1024]
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&models.DomainTeardown{}).
		Where("domain_name = ?", domainName).
		Updates(map[string]any{
			"attempts":        gorm.Expr("attempts + 1"),
			"last_error":      lastError,
			"last_attempt_at": now,
		}).Error
}
