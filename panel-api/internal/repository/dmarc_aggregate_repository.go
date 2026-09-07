package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DMARCAggregateRepository is append-only: rows are written by the
// Wave-6 ingest source and read by the per-domain dashboard / the
// deliverability score widget (Wave 9). PruneOlderThan implements the
// 90-day retention noted on mig 000139.
type DMARCAggregateRepository interface {
	// InsertMany bulk-inserts rows. Each row gets a fresh ULID + the
	// ingest-time `CreatedAt`. Empty `rows` is a no-op (the ingest
	// loop calls this once per report regardless of `<record>` count).
	InsertMany(ctx context.Context, rows []models.DMARCAggregate) (int, error)

	// ExistsForReport short-circuits ingest when a report (identified
	// by reporter + window) has already been imported. RUA messages
	// can be re-delivered (server retries, operator backfill) so the
	// ingest source MUST gate on this.
	ExistsForReport(ctx context.Context, reporter string, windowStart, windowEnd time.Time) (bool, error)

	ListByDomainSince(ctx context.Context, domain string, since time.Time) ([]models.DMARCAggregate, error)

	// PruneOlderThan deletes rows with window_end < cutoff. Returns the
	// number removed. Called by the retention reconciler.
	PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	// MostRecentWindowEnd returns the latest window_end stored. The
	// ingest source uses it as the cursor for incremental polls.
	MostRecentWindowEnd(ctx context.Context) (time.Time, error)
	// CountFailuresSince counts dkim=fail rows since the cutoff for the
	// deliverability score widget.
	CountFailuresSince(ctx context.Context, domain string, since time.Time) (int64, error)

	// ReKeyDomain moves every aggregate row from oldDomain to newDomain so an
	// in-place domain rename (GH #1579) keeps the DMARC dashboard's history
	// instead of orphaning it under the old name (the dashboard reads by domain
	// name). This is the ONLY UPDATE path on an otherwise append-only table, run
	// once per rename. Returns the rows moved.
	ReKeyDomain(ctx context.Context, oldDomain, newDomain string) (int64, error)
}

type dmarcRepo struct{ db *gorm.DB }

func NewDMARCAggregateRepository(db *gorm.DB) DMARCAggregateRepository {
	return &dmarcRepo{db: db}
}

func (r *dmarcRepo) InsertMany(ctx context.Context, rows []models.DMARCAggregate) (int, error) {
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

func (r *dmarcRepo) ExistsForReport(ctx context.Context, reporter string, windowStart, windowEnd time.Time) (bool, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&models.DMARCAggregate{}).
		Where("reporter = ? AND window_start = ? AND window_end = ?",
			reporter, windowStart.UTC(), windowEnd.UTC()).
		Limit(1).
		Count(&n).Error; err != nil {
		return false, translate(err)
	}
	return n > 0, nil
}

func (r *dmarcRepo) ListByDomainSince(ctx context.Context, domain string, since time.Time) ([]models.DMARCAggregate, error) {
	var rows []models.DMARCAggregate
	if err := r.db.WithContext(ctx).
		Where("domain = ? AND window_end >= ?", domain, since.UTC()).
		Order("window_end DESC").
		Find(&rows).Error; err != nil {
		return nil, translate(err)
	}
	return rows, nil
}

func (r *dmarcRepo) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("window_end < ?", cutoff.UTC()).
		Delete(&models.DMARCAggregate{})
	if res.Error != nil {
		return 0, translate(res.Error)
	}
	return res.RowsAffected, nil
}

func (r *dmarcRepo) MostRecentWindowEnd(ctx context.Context) (time.Time, error) {
	var row models.DMARCAggregate
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

func (r *dmarcRepo) ReKeyDomain(ctx context.Context, oldDomain, newDomain string) (int64, error) {
	// No unique key spans `domain`, so moving rows onto an existing name never
	// collides; ingest still dedupes app-side via ExistsForReport. A no-match
	// (domain never received a DMARC report) updates zero rows — a clean no-op.
	res := r.db.WithContext(ctx).Model(&models.DMARCAggregate{}).
		Where("domain = ?", oldDomain).
		Update("domain", newDomain)
	if res.Error != nil {
		return 0, translate(res.Error)
	}
	return res.RowsAffected, nil
}

func (r *dmarcRepo) CountFailuresSince(ctx context.Context, domain string, since time.Time) (int64, error) {
	var count int64
	q := r.db.WithContext(ctx).Model(&models.DMARCAggregate{}).
		Where("window_end >= ? AND dkim = ?", since.UTC(), "fail")
	if domain != "" {
		q = q.Where("domain = ?", domain)
	}
	if err := q.Count(&count).Error; err != nil {
		return 0, translate(err)
	}
	return count, nil
}
