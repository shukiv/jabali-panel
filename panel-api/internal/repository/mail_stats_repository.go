package repository

// GH #873: storage for the mail-statistics samples the mail stats ticker
// collects from Stalwart's Prometheus exporter.

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// MailStatSample is one (metric, time, value) point.
type MailStatSample struct {
	Metric    string    `gorm:"column:metric" json:"metric"`
	SampledAt time.Time `gorm:"column:sampled_at" json:"sampled_at"`
	Value     float64   `gorm:"column:value" json:"value"`
}

func (MailStatSample) TableName() string { return "mail_stats_samples" }

// MailboxStorageRow is one mailbox with its owner chain, for the
// Global -> User -> Domain -> Mailbox storage drilldown.
type MailboxStorageRow struct {
	Username   string `gorm:"column:username"    json:"username"`
	DomainName string `gorm:"column:domain_name" json:"domain_name"`
	Email      string `gorm:"column:email"       json:"email"`
	UsageBytes uint64 `gorm:"column:usage_bytes" json:"usage_bytes"`
	QuotaBytes uint64 `gorm:"column:quota_bytes" json:"quota_bytes"`
}

// DomainStatSample is one per-domain traffic delta point (GH #873 round 3).
// Value is the count of `Metric` events (sent|received|delivered|failed)
// attributed to `Domain` in the window ending at `SampledAt`.
type DomainStatSample struct {
	Domain    string    `gorm:"column:domain"     json:"domain"`
	Metric    string    `gorm:"column:metric"     json:"metric"`
	SampledAt time.Time `gorm:"column:sampled_at" json:"sampled_at"`
	Value     int64     `gorm:"column:value"      json:"value"`
}

func (DomainStatSample) TableName() string { return "mail_stats_domain_samples" }

// DomainCount is one (domain, metric, count) delta to persist for a window.
type DomainCount struct {
	Domain string
	Metric string
	Count  int64
}

type MailStatsRepository interface {
	// InsertSamples writes one row per metric at the given time.
	InsertSamples(ctx context.Context, at time.Time, metrics map[string]float64) error
	// Series returns all samples since `since`, ordered by metric then time.
	Series(ctx context.Context, since time.Time) ([]MailStatSample, error)
	// Prune deletes samples older than `olderThan`; returns rows removed.
	Prune(ctx context.Context, olderThan time.Time) (int64, error)
	// StorageDrilldown returns every mailbox with its usage and owner
	// chain (usage is the mailbox-usage ticker's last sample).
	StorageDrilldown(ctx context.Context) ([]MailboxStorageRow, error)

	// InsertDomainSamples writes the per-domain traffic deltas for one window
	// (all rows share `at`). No-op on an empty slice.
	InsertDomainSamples(ctx context.Context, at time.Time, counts []DomainCount) error
	// DomainSeries returns per-domain samples since `since`, ordered by
	// domain, metric, then time.
	DomainSeries(ctx context.Context, since time.Time) ([]DomainStatSample, error)
	// LastDomainSampleAt returns the newest domain sample time, or zero time
	// when the table is empty (first run). Drives the collector's watermark.
	LastDomainSampleAt(ctx context.Context) (time.Time, error)
	// PruneDomain deletes per-domain samples older than `olderThan`.
	PruneDomain(ctx context.Context, olderThan time.Time) (int64, error)
}

type mailStatsRepo struct{ db *gorm.DB }

func NewMailStatsRepository(db *gorm.DB) MailStatsRepository {
	return &mailStatsRepo{db: db}
}

func (r *mailStatsRepo) InsertSamples(ctx context.Context, at time.Time, metrics map[string]float64) error {
	if len(metrics) == 0 {
		return nil
	}
	rows := make([]MailStatSample, 0, len(metrics))
	for m, v := range metrics {
		rows = append(rows, MailStatSample{Metric: m, SampledAt: at, Value: v})
	}
	// Batch insert; the PK makes an accidental double-tick a conflict, not
	// a duplicate — ignore those.
	return r.db.WithContext(ctx).
		Exec("INSERT IGNORE INTO mail_stats_samples (metric, sampled_at, value) VALUES "+
			placeholders(len(rows)), flatten(rows)...).Error
}

func placeholders(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += "(?,?,?)"
	}
	return out
}

func flatten(rows []MailStatSample) []any {
	out := make([]any, 0, len(rows)*3)
	for _, r := range rows {
		out = append(out, r.Metric, r.SampledAt, r.Value)
	}
	return out
}

func (r *mailStatsRepo) Series(ctx context.Context, since time.Time) ([]MailStatSample, error) {
	var rows []MailStatSample
	err := r.db.WithContext(ctx).
		Where("sampled_at >= ?", since).
		Order("metric ASC, sampled_at ASC").
		Find(&rows).Error
	return rows, err
}

func (r *mailStatsRepo) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("sampled_at < ?", olderThan).
		Delete(&MailStatSample{})
	return res.RowsAffected, res.Error
}

func (r *mailStatsRepo) InsertDomainSamples(ctx context.Context, at time.Time, counts []DomainCount) error {
	if len(counts) == 0 {
		return nil
	}
	vals := make([]any, 0, len(counts)*4)
	ph := ""
	for i, c := range counts {
		if i > 0 {
			ph += ","
		}
		ph += "(?,?,?,?)"
		vals = append(vals, c.Domain, c.Metric, at, c.Count)
	}
	// INSERT IGNORE so a retried tick at the same sampled_at is a no-op rather
	// than a double-count.
	return r.db.WithContext(ctx).
		Exec("INSERT IGNORE INTO mail_stats_domain_samples (domain, metric, sampled_at, value) VALUES "+ph, vals...).Error
}

func (r *mailStatsRepo) DomainSeries(ctx context.Context, since time.Time) ([]DomainStatSample, error) {
	var rows []DomainStatSample
	err := r.db.WithContext(ctx).
		Where("sampled_at >= ?", since).
		Order("domain ASC, metric ASC, sampled_at ASC").
		Find(&rows).Error
	return rows, err
}

func (r *mailStatsRepo) LastDomainSampleAt(ctx context.Context) (time.Time, error) {
	// sql.NullTime (not *time.Time) so an empty table's NULL MAX() scans
	// cleanly on MariaDB rather than relying on GORM's **time.Time handling.
	var nt sql.NullTime
	err := r.db.WithContext(ctx).
		Model(&DomainStatSample{}).
		Select("MAX(sampled_at)").
		Scan(&nt).Error
	if err != nil || !nt.Valid {
		return time.Time{}, err
	}
	return nt.Time, nil
}

func (r *mailStatsRepo) PruneDomain(ctx context.Context, olderThan time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("sampled_at < ?", olderThan).
		Delete(&DomainStatSample{})
	return res.RowsAffected, res.Error
}

func (r *mailStatsRepo) StorageDrilldown(ctx context.Context) ([]MailboxStorageRow, error) {
	var rows []MailboxStorageRow
	err := r.db.WithContext(ctx).Raw(`
		SELECT u.username AS username, d.name AS domain_name,
		       m.email_cached AS email, m.last_usage_bytes AS usage_bytes,
		       m.quota_bytes AS quota_bytes
		FROM mailboxes m
		JOIN domains d ON d.id = m.domain_id
		JOIN users   u ON u.id = d.user_id
		ORDER BY u.username, d.name, m.email_cached`).Scan(&rows).Error
	return rows, err
}
