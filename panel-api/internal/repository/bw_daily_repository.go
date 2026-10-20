package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// BWDailyRepository persists per-domain daily bandwidth totals from
// goaccess scans. The "current" view (today + last 30 days) is what
// the panel UI cares about; older history can be pruned by an
// operator-driven CLI in a future milestone.
type BWDailyRepository interface {
	// Upsert writes one (domain_id, day) row. Re-runs of the agent
	// scan against the same yesterday log are expected — caller does
	// not need to read-then-write.
	Upsert(ctx context.Context, row *models.BWDaily) error

	// SumForDomain returns total bytes + requests over [from, to] for
	// one domain. Inclusive on both ends. Pass zero-value `to` for
	// "now".
	SumForDomain(ctx context.Context, domainID string, from, to time.Time) (bytes, reqs uint64, err error)

	// SumByDomainForUser returns one row per domain owned by user_id
	// over [from, to]. Used by the Users list to render a "Bandwidth
	// (this month)" column.
	SumByDomainForUser(ctx context.Context, userID string, from, to time.Time) (map[string]uint64, error)

	// SumByDomainIDs is the batch-by-id variant for the admin domain
	// list — caller already has the page's domain IDs, so the
	// owner-side restriction is unnecessary. Returns map keyed by
	// domain_id; absent keys mean zero traffic.
	SumByDomainIDs(ctx context.Context, ids []string, from, to time.Time) (map[string]uint64, error)

	// SumPerDayForDomain returns daily series (date → bytes) for a
	// single domain over [from, to]. Drives sparkline charts on the
	// Domain detail card.
	SumPerDayForDomain(ctx context.Context, domainID string, from, to time.Time) ([]DailyPoint, error)

	// ReplaceCountriesForDay writes one domain's per-country rows for one
	// day, replacing the rows that day already had (a re-scan of the same
	// log).
	ReplaceCountriesForDay(ctx context.Context, domainID string, day time.Time, rows []models.BWDailyCountry) error

	// SumCountriesForDomain returns one domain's traffic per country over
	// [from, to], most requests first. Inclusive on both ends; zero `to`
	// means "now".
	SumCountriesForDomain(ctx context.Context, domainID string, from, to time.Time) ([]CountryPoint, error)
}

// DailyPoint is one day's data point for sparkline + chart rendering.
type DailyPoint struct {
	Day           time.Time `json:"day"`
	BytesTotal    uint64    `json:"bytes_total"`
	RequestsTotal uint64    `json:"requests_total"`
}

// CountryPoint is one country's share of a domain's traffic over a window.
type CountryPoint struct {
	CountryCode   string `json:"country_code"`
	BytesTotal    uint64 `json:"bytes_total"`
	RequestsTotal uint64 `json:"requests_total"`
}

type bwDailyRepo struct{ db *gorm.DB }

func NewBWDailyRepository(db *gorm.DB) BWDailyRepository { return &bwDailyRepo{db: db} }

func (r *bwDailyRepo) Upsert(ctx context.Context, row *models.BWDaily) error {
	row.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "domain_id"}, {Name: "day"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"bytes_total", "requests_total", "updated_at",
			}),
		}).
		Create(row).Error
}

func (r *bwDailyRepo) SumForDomain(ctx context.Context, domainID string, from, to time.Time) (uint64, uint64, error) {
	var out struct {
		Bytes uint64 `gorm:"column:bytes"`
		Reqs  uint64 `gorm:"column:reqs"`
	}
	q := r.db.WithContext(ctx).Table("bw_daily").
		Select("COALESCE(SUM(bytes_total),0) AS bytes, COALESCE(SUM(requests_total),0) AS reqs").
		Where("domain_id = ?", domainID).
		Where("day >= ?", from)
	if !to.IsZero() {
		q = q.Where("day <= ?", to)
	}
	if err := q.Scan(&out).Error; err != nil {
		return 0, 0, err
	}
	return out.Bytes, out.Reqs, nil
}

func (r *bwDailyRepo) SumByDomainForUser(ctx context.Context, userID string, from, to time.Time) (map[string]uint64, error) {
	var rows []struct {
		DomainID string `gorm:"column:domain_id"`
		Bytes    uint64 `gorm:"column:bytes"`
	}
	q := r.db.WithContext(ctx).Table("bw_daily").
		Joins("JOIN domains ON domains.id = bw_daily.domain_id").
		Select("bw_daily.domain_id AS domain_id, COALESCE(SUM(bw_daily.bytes_total),0) AS bytes").
		Where("domains.user_id = ?", userID).
		Where("bw_daily.day >= ?", from).
		Group("bw_daily.domain_id")
	if !to.IsZero() {
		q = q.Where("bw_daily.day <= ?", to)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(rows))
	for _, r := range rows {
		out[r.DomainID] = r.Bytes
	}
	return out, nil
}

func (r *bwDailyRepo) SumByDomainIDs(ctx context.Context, ids []string, from, to time.Time) (map[string]uint64, error) {
	if len(ids) == 0 {
		return map[string]uint64{}, nil
	}
	var rows []struct {
		DomainID string `gorm:"column:domain_id"`
		Bytes    uint64 `gorm:"column:bytes"`
	}
	q := r.db.WithContext(ctx).Table("bw_daily").
		Select("domain_id, COALESCE(SUM(bytes_total),0) AS bytes").
		Where("domain_id IN ?", ids).
		Where("day >= ?", from).
		Group("domain_id")
	if !to.IsZero() {
		q = q.Where("day <= ?", to)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(rows))
	for _, r := range rows {
		out[r.DomainID] = r.Bytes
	}
	return out, nil
}

func (r *bwDailyRepo) SumPerDayForDomain(ctx context.Context, domainID string, from, to time.Time) ([]DailyPoint, error) {
	var rows []DailyPoint
	q := r.db.WithContext(ctx).Table("bw_daily").
		Select("day, bytes_total, requests_total").
		Where("domain_id = ?", domainID).
		Where("day >= ?", from).
		Order("day ASC")
	if !to.IsZero() {
		q = q.Where("day <= ?", to)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *bwDailyRepo) ReplaceCountriesForDay(ctx context.Context, domainID string, day time.Time, rows []models.BWDailyCountry) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("domain_id = ? AND day = ?", domainID, day).
			Delete(&models.BWDailyCountry{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for i := range rows {
			rows[i].DomainID = domainID
			rows[i].Day = day
			rows[i].UpdatedAt = now
		}
		return tx.Create(&rows).Error
	})
}

func (r *bwDailyRepo) SumCountriesForDomain(ctx context.Context, domainID string, from, to time.Time) ([]CountryPoint, error) {
	var rows []CountryPoint
	q := r.db.WithContext(ctx).Table("bw_daily_country").
		Select("country_code, COALESCE(SUM(bytes_total),0) AS bytes_total, COALESCE(SUM(requests_total),0) AS requests_total").
		Where("domain_id = ?", domainID).
		Where("day >= ?", from).
		Group("country_code").
		Order("requests_total DESC, country_code ASC")
	if !to.IsZero() {
		q = q.Where("day <= ?", to)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
