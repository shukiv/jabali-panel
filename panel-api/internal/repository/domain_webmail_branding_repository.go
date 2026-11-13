package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainWebmailBrandingRepository persists domains' webmail branding (GH
// #2502).
type DomainWebmailBrandingRepository interface {
	// Get returns the domain's branding; ErrNotFound when it has none.
	Get(ctx context.Context, domainID string) (*models.DomainWebmailBranding, error)
	// Put stores the domain's branding, every field of it.
	Put(ctx context.Context, row *models.DomainWebmailBranding) error
	// Delete removes the domain's branding. No branding is not an error.
	Delete(ctx context.Context, domainID string) error
	// ListAll returns every domain's branding with the domain's name and
	// whether it applies, by domain name.
	ListAll(ctx context.Context) ([]WebmailBrandingRow, error)
	// ListByDomainIDs returns the branding of those of the domains that
	// have one, for an account's backup.
	ListByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DomainWebmailBranding, error)
}

// WebmailBrandingRow is a domain's branding with the domain's name, and
// Applies: an administrator set it, or the owner's package has webmail
// branding. A user with no package has none (GH #282).
type WebmailBrandingRow struct {
	models.DomainWebmailBranding
	DomainName string `gorm:"column:domain_name"`
	Applies    bool   `gorm:"column:applies"`
}

type domainWebmailBrandingRepo struct{ db *gorm.DB }

func NewDomainWebmailBrandingRepository(db *gorm.DB) DomainWebmailBrandingRepository {
	return &domainWebmailBrandingRepo{db: db}
}

func (r *domainWebmailBrandingRepo) Get(ctx context.Context, domainID string) (*models.DomainWebmailBranding, error) {
	var row models.DomainWebmailBranding
	err := r.db.WithContext(ctx).Where("domain_id = ?", domainID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *domainWebmailBrandingRepo) Put(ctx context.Context, row *models.DomainWebmailBranding) error {
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "domain_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"app_name", "company_name", "website_url", "privacy_url", "imprint_url",
			"logo_light", "logo_dark", "favicon", "set_by_admin", "updated_at",
		}),
	}).Create(row).Error
	return translate(err)
}

func (r *domainWebmailBrandingRepo) Delete(ctx context.Context, domainID string) error {
	return r.db.WithContext(ctx).Where("domain_id = ?", domainID).Delete(&models.DomainWebmailBranding{}).Error
}

func (r *domainWebmailBrandingRepo) ListAll(ctx context.Context) ([]WebmailBrandingRow, error) {
	var rows []WebmailBrandingRow
	err := r.db.WithContext(ctx).
		Table("domain_webmail_branding AS b").
		Select("b.*, d.name AS domain_name, " +
			"(b.set_by_admin = 1 OR COALESCE(p.webmail_branding_enabled, 0) = 1) AS applies").
		Joins("JOIN domains d ON d.id = b.domain_id").
		Joins("JOIN users u ON u.id = d.user_id").
		Joins("LEFT JOIN hosting_packages p ON p.id = u.package_id").
		Order("d.name").
		Scan(&rows).Error
	return rows, err
}

func (r *domainWebmailBrandingRepo) ListByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DomainWebmailBranding, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	var rows []models.DomainWebmailBranding
	err := r.db.WithContext(ctx).Where("domain_id IN ?", domainIDs).Order("domain_id").Find(&rows).Error
	return rows, err
}
