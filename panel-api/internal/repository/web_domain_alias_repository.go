package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// WebDomainAliasRepository persists the additional hostnames served from
// a web domain's vhost (GH #1625). The reconciler reads
// ListHostnamesByDomainID every time it converges a domain to render the
// server_name list and the cert SAN set.
type WebDomainAliasRepository interface {
	Create(ctx context.Context, row *models.WebDomainAlias) error
	FindByID(ctx context.Context, id string) (*models.WebDomainAlias, error)
	ListByDomain(ctx context.Context, domainID string) ([]models.WebDomainAlias, error)
	// ListHostnamesByDomainID returns just the lowercased hostnames for
	// one domain, ordered stably — the reconciler's single source of
	// truth for a domain's aliases on a converge pass.
	ListHostnamesByDomainID(ctx context.Context, domainID string) ([]string, error)
	// FindByHostname resolves an alias by its (globally unique) hostname,
	// or ErrNotFound when the name is free. The create handler uses it to
	// enforce global uniqueness before insert (the DB index is the
	// backstop, this is the friendly 409).
	FindByHostname(ctx context.Context, hostname string) (*models.WebDomainAlias, error)
	Delete(ctx context.Context, id string) error
}

type webDomainAliasRepo struct{ db *gorm.DB }

func NewWebDomainAliasRepository(db *gorm.DB) WebDomainAliasRepository {
	return &webDomainAliasRepo{db: db}
}

func (r *webDomainAliasRepo) Create(ctx context.Context, row *models.WebDomainAlias) error {
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = now
	}
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *webDomainAliasRepo) FindByID(ctx context.Context, id string) (*models.WebDomainAlias, error) {
	var row models.WebDomainAlias
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *webDomainAliasRepo) ListByDomain(ctx context.Context, domainID string) ([]models.WebDomainAlias, error) {
	var rows []models.WebDomainAlias
	err := r.db.WithContext(ctx).
		Where("domain_id = ?", domainID).
		Order("hostname ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *webDomainAliasRepo) ListHostnamesByDomainID(ctx context.Context, domainID string) ([]string, error) {
	var names []string
	err := r.db.WithContext(ctx).
		Model(&models.WebDomainAlias{}).
		Where("domain_id = ?", domainID).
		Order("hostname ASC").
		Pluck("hostname", &names).Error
	if err != nil {
		return nil, err
	}
	for i, n := range names {
		names[i] = strings.ToLower(strings.TrimSpace(n))
	}
	return names, nil
}

func (r *webDomainAliasRepo) FindByHostname(ctx context.Context, hostname string) (*models.WebDomainAlias, error) {
	var row models.WebDomainAlias
	err := r.db.WithContext(ctx).First(&row, "hostname = ?", strings.ToLower(strings.TrimSpace(hostname))).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *webDomainAliasRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&models.WebDomainAlias{}, "id = ?", id).Error
}
