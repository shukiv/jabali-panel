package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainDirectoryPrivacyRepository persists per-domain HTTP Basic Auth
// rules + credentials (M50). Reconciler reads ListRulesWithCreds when
// converging a domain so the agent can write htpasswd files + render
// the vhost auth_basic location blocks.
type DomainDirectoryPrivacyRepository interface {
	CreateRule(ctx context.Context, row *models.DomainDirectoryPrivacyRule) error
	UpdateRule(ctx context.Context, id string, realm string) error
	FindRuleByID(ctx context.Context, id string) (*models.DomainDirectoryPrivacyRule, error)
	ListRulesByDomain(ctx context.Context, domainID string) ([]models.DomainDirectoryPrivacyRule, error)
	DeleteRule(ctx context.Context, id string) error

	CreateCredential(ctx context.Context, row *models.DomainDirectoryPrivacyCredential) error
	FindCredentialByID(ctx context.Context, id string) (*models.DomainDirectoryPrivacyCredential, error)
	ListCredentialsByRule(ctx context.Context, ruleID string) ([]models.DomainDirectoryPrivacyCredential, error)
	DeleteCredential(ctx context.Context, id string) error
}

type domainDirectoryPrivacyRepo struct{ db *gorm.DB }

func NewDomainDirectoryPrivacyRepository(db *gorm.DB) DomainDirectoryPrivacyRepository {
	return &domainDirectoryPrivacyRepo{db: db}
}

func (r *domainDirectoryPrivacyRepo) CreateRule(ctx context.Context, row *models.DomainDirectoryPrivacyRule) error {
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = now
	}
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *domainDirectoryPrivacyRepo) UpdateRule(ctx context.Context, id string, realm string) error {
	res := r.db.WithContext(ctx).
		Model(&models.DomainDirectoryPrivacyRule{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"realm":      realm,
			"updated_at": time.Now().UTC(),
		})
	if err := res.Error; err != nil {
		return err
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainDirectoryPrivacyRepo) FindRuleByID(ctx context.Context, id string) (*models.DomainDirectoryPrivacyRule, error) {
	var row models.DomainDirectoryPrivacyRule
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *domainDirectoryPrivacyRepo) ListRulesByDomain(ctx context.Context, domainID string) ([]models.DomainDirectoryPrivacyRule, error) {
	var rows []models.DomainDirectoryPrivacyRule
	err := r.db.WithContext(ctx).
		Where("domain_id = ?", domainID).
		Order("path ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainDirectoryPrivacyRepo) DeleteRule(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.DomainDirectoryPrivacyRule{}, "id = ?", id)
	if err := res.Error; err != nil {
		return err
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainDirectoryPrivacyRepo) CreateCredential(ctx context.Context, row *models.DomainDirectoryPrivacyCredential) error {
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *domainDirectoryPrivacyRepo) FindCredentialByID(ctx context.Context, id string) (*models.DomainDirectoryPrivacyCredential, error) {
	var row models.DomainDirectoryPrivacyCredential
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *domainDirectoryPrivacyRepo) ListCredentialsByRule(ctx context.Context, ruleID string) ([]models.DomainDirectoryPrivacyCredential, error) {
	var rows []models.DomainDirectoryPrivacyCredential
	err := r.db.WithContext(ctx).
		Where("rule_id = ?", ruleID).
		Order("username ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainDirectoryPrivacyRepo) DeleteCredential(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.DomainDirectoryPrivacyCredential{}, "id = ?", id)
	if err := res.Error; err != nil {
		return err
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
