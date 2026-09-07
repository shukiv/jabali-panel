package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TLSRPTAggregateRepository is append-only — see the DMARC sibling for
// the operational discipline. Cursor = MostRecentWindowEnd.
type TLSRPTAggregateRepository interface {
	InsertMany(ctx context.Context, rows []models.TLSRPTAggregate) (int, error)
	ExistsForReport(ctx context.Context, reporter string, windowStart, windowEnd time.Time) (bool, error)
	ListByDomainSince(ctx context.Context, domain string, since time.Time) ([]models.TLSRPTAggregate, error)
	CountFailuresSince(ctx context.Context, domain string, since time.Time) (int64, error)
	PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	MostRecentWindowEnd(ctx context.Context) (time.Time, error)

	// ReKeyDomain moves every aggregate row from oldDomain to newDomain so an
	// in-place domain rename (GH #1579) keeps the TLS-RPT dashboard's history
	// instead of orphaning it under the old name (the dashboard reads by domain
	// name). Sole UPDATE path on an otherwise append-only table; returns the
	// rows moved. Mirrors the DMARC sibling.
	ReKeyDomain(ctx context.Context, oldDomain, newDomain string) (int64, error)
}

type tlsRptRepo struct{ db *gorm.DB }

func NewTLSRPTAggregateRepository(db *gorm.DB) TLSRPTAggregateRepository {
	return &tlsRptRepo{db: db}
}

func (r *tlsRptRepo) InsertMany(ctx context.Context, rows []models.TLSRPTAggregate) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	for i := range rows {
		if rows[i].ID == "" {
			rows[i].ID = ids.NewULID()
		}
		if rows[i].CreatedAt.IsZero() {
			rows[i].CreatedAt = now
		}
	}
	if err := r.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return 0, translate(err)
	}
	return len(rows), nil
}

func (r *tlsRptRepo) ReKeyDomain(ctx context.Context, oldDomain, newDomain string) (int64, error) {
	// No unique key spans `domain`, so moving rows onto an existing name never
	// collides; ingest still dedupes app-side via ExistsForReport. A no-match
	// (domain never received a TLS-RPT report) updates zero rows — a clean no-op.
	res := r.db.WithContext(ctx).Model(&models.TLSRPTAggregate{}).
		Where("domain = ?", oldDomain).
		Update("domain", newDomain)
	if res.Error != nil {
		return 0, translate(res.Error)
	}
	return res.RowsAffected, nil
}

func (r *tlsRptRepo) ExistsForReport(ctx context.Context, reporter string, windowStart, windowEnd time.Time) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.TLSRPTAggregate{}).
		Where("reporter = ? AND window_start = ? AND window_end = ?", reporter, windowStart.UTC(), windowEnd.UTC()).
		Count(&count).Error; err != nil {
		return false, translate(err)
	}
	return count > 0, nil
}

func (r *tlsRptRepo) ListByDomainSince(ctx context.Context, domain string, since time.Time) ([]models.TLSRPTAggregate, error) {
	var rows []models.TLSRPTAggregate
	if err := r.db.WithContext(ctx).
		Where("domain = ? AND window_end >= ?", domain, since).
		Order("window_end DESC").
		Find(&rows).Error; err != nil {
		return nil, translate(err)
	}
	return rows, nil
}

func (r *tlsRptRepo) CountFailuresSince(ctx context.Context, domain string, since time.Time) (int64, error) {
	var sum int64
	q := r.db.WithContext(ctx).Model(&models.TLSRPTAggregate{}).
		Select("COALESCE(SUM(failure_count), 0)").
		Where("window_end >= ?", since)
	if domain != "" {
		q = q.Where("domain = ?", domain)
	}
	row := q.Row()
	if err := row.Scan(&sum); err != nil {
		return 0, translate(err)
	}
	return sum, nil
}

func (r *tlsRptRepo) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("window_end < ?", cutoff).
		Delete(&models.TLSRPTAggregate{})
	if res.Error != nil {
		return 0, translate(res.Error)
	}
	return res.RowsAffected, nil
}

func (r *tlsRptRepo) MostRecentWindowEnd(ctx context.Context) (time.Time, error) {
	var row models.TLSRPTAggregate
	err := r.db.WithContext(ctx).
		Select("window_end").
		Order("window_end DESC").
		Limit(1).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, translate(err)
	}
	return row.WindowEnd, nil
}
