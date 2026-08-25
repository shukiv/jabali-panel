package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DNSZoneRepository covers the dns_zones table. ListAll is used by the
// reconciler for periodic full-sync sweeps; FindByDomainID is the hot
// path when a domain changes and needs its zone re-pushed.
type DNSZoneRepository interface {
	Create(ctx context.Context, z *models.DNSZone) error
	Update(ctx context.Context, z *models.DNSZone) error
	Delete(ctx context.Context, id string) error
	FindByID(ctx context.Context, id string) (*models.DNSZone, error)
	FindByName(ctx context.Context, name string) (*models.DNSZone, error)
	FindByDomainID(ctx context.Context, domainID string) (*models.DNSZone, error)
	// FindByDomainIDs returns the zones for the given domain IDs in one query —
	// the DNS Zone inventory's provisioned + zone-id lookup (JAB-377). Domains
	// without a zone simply have no entry; the caller treats absence as
	// not-provisioned. Empty input returns nil without a query.
	FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DNSZone, error)
	ListAll(ctx context.Context) ([]models.DNSZone, error)
}

// DNSRecordRepository covers dns_records. All mutations are scoped to a
// zone because zone is the unit the agent pushes atomically.
type DNSRecordRepository interface {
	Create(ctx context.Context, r *models.DNSRecord) error
	Update(ctx context.Context, r *models.DNSRecord) error
	Delete(ctx context.Context, id string) error
	FindByID(ctx context.Context, id string) (*models.DNSRecord, error)
	ListByZoneID(ctx context.Context, zoneID string) ([]models.DNSRecord, error)
	// CountByZoneIDs returns the user-record count per zone in one aggregate
	// query (COUNT(*) GROUP BY zone_id) — the DNS Zone inventory's record-count
	// column without loading a single record payload (JAB-377). Zones with no
	// records are absent from the map (caller defaults to 0). Empty input
	// returns an empty map without a query.
	CountByZoneIDs(ctx context.Context, zoneIDs []string) (map[string]int64, error)
	DeleteByZoneID(ctx context.Context, zoneID string) error
	// DeleteByZoneIDAndManagedBy removes every row in the given zone
	// whose managed_by column matches exactly — NULL values are never
	// touched. Used by M6 on domain.email_disable to clean up the
	// email-specific records (DKIM, autoconfig, autodiscover) without
	// disturbing M4 bootstrap records or user-edited rows.
	DeleteByZoneIDAndManagedBy(ctx context.Context, zoneID, managedBy string) error
}

type dnsZoneRepo struct{ db *gorm.DB }

func NewDNSZoneRepository(db *gorm.DB) DNSZoneRepository {
	return &dnsZoneRepo{db: db}
}

func (r *dnsZoneRepo) Create(ctx context.Context, z *models.DNSZone) error {
	return r.db.WithContext(ctx).Create(z).Error
}

func (r *dnsZoneRepo) Update(ctx context.Context, z *models.DNSZone) error {
	return r.db.WithContext(ctx).Save(z).Error
}

func (r *dnsZoneRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&models.DNSZone{}, "id = ?", id).Error
}

func (r *dnsZoneRepo) FindByID(ctx context.Context, id string) (*models.DNSZone, error) {
	var z models.DNSZone
	err := r.db.WithContext(ctx).First(&z, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &z, nil
}

func (r *dnsZoneRepo) FindByName(ctx context.Context, name string) (*models.DNSZone, error) {
	var z models.DNSZone
	err := r.db.WithContext(ctx).First(&z, "name = ?", name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &z, nil
}

func (r *dnsZoneRepo) FindByDomainID(ctx context.Context, domainID string) (*models.DNSZone, error) {
	var z models.DNSZone
	err := r.db.WithContext(ctx).First(&z, "domain_id = ?", domainID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &z, nil
}

func (r *dnsZoneRepo) FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DNSZone, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	var zs []models.DNSZone
	if err := r.db.WithContext(ctx).Where("domain_id IN ?", domainIDs).Find(&zs).Error; err != nil {
		return nil, err
	}
	return zs, nil
}

func (r *dnsZoneRepo) ListAll(ctx context.Context) ([]models.DNSZone, error) {
	var zs []models.DNSZone
	if err := r.db.WithContext(ctx).Find(&zs).Error; err != nil {
		return nil, err
	}
	return zs, nil
}

type dnsRecordRepo struct{ db *gorm.DB }

func NewDNSRecordRepository(db *gorm.DB) DNSRecordRepository {
	return &dnsRecordRepo{db: db}
}

func (r *dnsRecordRepo) Create(ctx context.Context, rec *models.DNSRecord) error {
	return r.db.WithContext(ctx).Create(rec).Error
}

func (r *dnsRecordRepo) Update(ctx context.Context, rec *models.DNSRecord) error {
	return r.db.WithContext(ctx).Save(rec).Error
}

func (r *dnsRecordRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&models.DNSRecord{}, "id = ?", id).Error
}

func (r *dnsRecordRepo) FindByID(ctx context.Context, id string) (*models.DNSRecord, error) {
	var rec models.DNSRecord
	err := r.db.WithContext(ctx).First(&rec, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *dnsRecordRepo) ListByZoneID(ctx context.Context, zoneID string) ([]models.DNSRecord, error) {
	var recs []models.DNSRecord
	err := r.db.WithContext(ctx).
		Where("zone_id = ?", zoneID).
		Order("type, name, id").
		Find(&recs).Error
	if err != nil {
		return nil, err
	}
	return recs, nil
}

func (r *dnsRecordRepo) CountByZoneIDs(ctx context.Context, zoneIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(zoneIDs))
	if len(zoneIDs) == 0 {
		return out, nil
	}
	type countRow struct {
		ZoneID string `gorm:"column:zone_id"`
		Cnt    int64  `gorm:"column:cnt"`
	}
	var rows []countRow
	if err := r.db.WithContext(ctx).
		Model(&models.DNSRecord{}).
		Select("zone_id, COUNT(*) AS cnt").
		Where("zone_id IN ?", zoneIDs).
		Group("zone_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, x := range rows {
		out[x.ZoneID] = x.Cnt
	}
	return out, nil
}

func (r *dnsRecordRepo) DeleteByZoneID(ctx context.Context, zoneID string) error {
	return r.db.WithContext(ctx).
		Where("zone_id = ?", zoneID).
		Delete(&models.DNSRecord{}).Error
}

func (r *dnsRecordRepo) DeleteByZoneIDAndManagedBy(ctx context.Context, zoneID, managedBy string) error {
	return r.db.WithContext(ctx).
		Where("zone_id = ? AND managed_by = ?", zoneID, managedBy).
		Delete(&models.DNSRecord{}).Error
}
