package repository

import (
	"context"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DNSSECKeyRepository caches DNSSEC keys observed via `pdnsutil show-zone`.
// Authoritative data lives in PowerDNS. This cache enables the UI to render
// a "Keys" column without shelling out on every list render (ADR-0076).
type DNSSECKeyRepository interface {
	ListByDomainID(ctx context.Context, domainID string) ([]models.DomainDNSSECKey, error)
	// ListByDomainIDs batch-loads DNSSEC keys for many domains (JAB-374),
	// ordered domain_id, key_type, key_tag to match ListByDomainID within a domain.
	ListByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DomainDNSSECKey, error)
	ReplaceAll(ctx context.Context, domainID string, keys []models.DomainDNSSECKey) error
	DeleteAllForDomain(ctx context.Context, domainID string) error
}

type dnssecKeyRepo struct {
	db *gorm.DB
}

// NewDNSSECKeyRepository returns a GORM-backed cache.
func NewDNSSECKeyRepository(db *gorm.DB) DNSSECKeyRepository {
	return &dnssecKeyRepo{db: db}
}

func (r *dnssecKeyRepo) ListByDomainID(ctx context.Context, domainID string) ([]models.DomainDNSSECKey, error) {
	var out []models.DomainDNSSECKey
	res := r.db.WithContext(ctx).
		Where("domain_id = ?", domainID).
		Order("key_type ASC, key_tag ASC").
		Find(&out)
	if res.Error != nil {
		return nil, translate(res.Error)
	}
	return out, nil
}

// ListByDomainIDs batch-loads DNSSEC keys for a set of domains in one query
// (JAB-374). ORDER BY reproduces ListByDomainID's key_type, key_tag within each
// domain so grouped output is golden-identical.
func (r *dnssecKeyRepo) ListByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DomainDNSSECKey, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	var out []models.DomainDNSSECKey
	if err := r.db.WithContext(ctx).Where("domain_id IN ?", domainIDs).
		Order("domain_id, key_type ASC, key_tag ASC").Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// ReplaceAll deletes every cached key for the domain and inserts the new set
// in one transaction. Empty input clears the cache for the domain.
func (r *dnssecKeyRepo) ReplaceAll(ctx context.Context, domainID string, keys []models.DomainDNSSECKey) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("domain_id = ?", domainID).Delete(&models.DomainDNSSECKey{}).Error; err != nil {
			return translate(err)
		}
		if len(keys) == 0 {
			return nil
		}
		for i := range keys {
			keys[i].DomainID = domainID
		}
		if err := tx.Create(&keys).Error; err != nil {
			return translate(err)
		}
		return nil
	})
}

func (r *dnssecKeyRepo) DeleteAllForDomain(ctx context.Context, domainID string) error {
	res := r.db.WithContext(ctx).
		Where("domain_id = ?", domainID).
		Delete(&models.DomainDNSSECKey{})
	if res.Error != nil {
		return translate(res.Error)
	}
	return nil
}
