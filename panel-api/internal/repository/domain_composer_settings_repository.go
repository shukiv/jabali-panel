package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainComposerSettingsRepository is data access for the Composer project
// folder a domain uses (GH #2276).
type DomainComposerSettingsRepository interface {
	// Get returns the domain's settings; ErrNotFound when it has none, so it
	// finds composer.json on its own.
	Get(ctx context.Context, domainID string) (*models.DomainComposerSettings, error)
	// Put stores the domain's settings, replacing earlier ones.
	Put(ctx context.Context, row *models.DomainComposerSettings) error
	// Delete removes the domain's settings. A domain with none is not an
	// error.
	Delete(ctx context.Context, domainID string) error
}

type domainComposerSettingsRepo struct {
	db *gorm.DB
}

func NewDomainComposerSettingsRepository(db *gorm.DB) DomainComposerSettingsRepository {
	return &domainComposerSettingsRepo{db: db}
}

func (r *domainComposerSettingsRepo) Get(ctx context.Context, domainID string) (*models.DomainComposerSettings, error) {
	var row models.DomainComposerSettings
	if err := r.db.WithContext(ctx).Where("domain_id = ?", domainID).First(&row).Error; err != nil {
		return nil, translate(err)
	}
	return &row, nil
}

func (r *domainComposerSettingsRepo) Put(ctx context.Context, row *models.DomainComposerSettings) error {
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	row.UpdatedAt = now
	return translate(r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"project_dir", "updated_at"}),
	}).Create(row).Error)
}

func (r *domainComposerSettingsRepo) Delete(ctx context.Context, domainID string) error {
	return r.db.WithContext(ctx).Where("domain_id = ?", domainID).Delete(&models.DomainComposerSettings{}).Error
}
