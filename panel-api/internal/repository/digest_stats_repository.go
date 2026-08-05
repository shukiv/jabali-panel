package repository

// GH #840: aggregation source for the daily digest email. One repository
// with a handful of read-only aggregate queries instead of widening four
// unrelated repositories — the digest is the only consumer.

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// DigestKindCount is one event kind's 24h volume.
type DigestKindCount struct {
	EventKind string `json:"event_kind"`
	Count     int64  `json:"count"`
}

// DigestStats is everything the daily digest reports on.
type DigestStats struct {
	// AlertsBySeverity counts notification_history rows since the window
	// start, keyed critical/error/warning/info. This rolls up everything
	// the panel already alerts on (cert failures, disk, service.down,
	// CrowdSec spikes, backups...) without re-querying each source.
	AlertsBySeverity map[string]int64
	// TopKinds is the five busiest event kinds in the window.
	TopKinds []DigestKindCount
	// BackupsByStatus counts backup_jobs created in the window by status.
	BackupsByStatus map[string]int64
	// CertsExpiring14d counts issued certificates expiring within 14 days.
	CertsExpiring14d int64
	DomainsTotal     int64
	DomainsEnabled   int64
	UsersTotal       int64

	// Mail volume for the window (GH #840), derived from the #873
	// mail_stats_samples counters. MailStatsAvailable is false on a
	// mail-less / pre-mail-stats install (table absent or no samples), in
	// which case the digest omits the mail line rather than reporting a
	// misleading zero. Received = message_ingest_ham + _spam; Sent =
	// authenticated submissions queued; QueueNow = latest queue gauge.
	MailStatsAvailable bool
	MailReceived       int64
	MailSent           int64
	MailQueueNow       int64
}

type DigestStatsRepository interface {
	Collect(ctx context.Context, since time.Time) (*DigestStats, error)
}

type digestStatsRepo struct{ db *gorm.DB }

func NewDigestStatsRepository(db *gorm.DB) DigestStatsRepository {
	return &digestStatsRepo{db: db}
}

func (r *digestStatsRepo) Collect(ctx context.Context, since time.Time) (*DigestStats, error) {
	out := &DigestStats{
		AlertsBySeverity: map[string]int64{},
		BackupsByStatus:  map[string]int64{},
	}
	db := r.db.WithContext(ctx)

	var sevRows []struct {
		Severity string
		N        int64
	}
	// Envelope-level dedupe: the dispatcher writes one history row per
	// TARGET (bell + each channel), so a single event with three channels
	// would count as three alerts. Distinct envelope_id collapses that;
	// rows without an envelope_id (legacy) count individually.
	if err := db.Raw(`SELECT severity, COUNT(DISTINCT COALESCE(envelope_id, id)) AS n
		FROM notification_history WHERE created_at >= ? GROUP BY severity`, since).
		Scan(&sevRows).Error; err != nil {
		return nil, err
	}
	for _, r := range sevRows {
		out.AlertsBySeverity[r.Severity] = r.N
	}

	var kindRows []struct {
		EventKind string
		N         int64
	}
	if err := db.Raw(`SELECT event_kind, COUNT(DISTINCT COALESCE(envelope_id, id)) AS n
		FROM notification_history WHERE created_at >= ? AND event_kind != 'digest.daily'
		GROUP BY event_kind ORDER BY n DESC LIMIT 5`, since).
		Scan(&kindRows).Error; err != nil {
		return nil, err
	}
	for _, k := range kindRows {
		out.TopKinds = append(out.TopKinds, DigestKindCount{EventKind: k.EventKind, Count: k.N})
	}

	var bkRows []struct {
		Status string
		N      int64
	}
	if err := db.Raw(`SELECT status, COUNT(*) AS n FROM backup_jobs
		WHERE created_at >= ? GROUP BY status`, since).
		Scan(&bkRows).Error; err != nil {
		return nil, err
	}
	for _, b := range bkRows {
		out.BackupsByStatus[b.Status] = b.N
	}

	if err := db.Raw(`SELECT COUNT(*) FROM ssl_certificates
		WHERE status = 'issued' AND expires_at IS NOT NULL
		AND expires_at BETWEEN NOW() AND DATE_ADD(NOW(), INTERVAL 14 DAY)`).
		Scan(&out.CertsExpiring14d).Error; err != nil {
		return nil, err
	}
	if err := db.Raw(`SELECT COUNT(*) FROM domains`).Scan(&out.DomainsTotal).Error; err != nil {
		return nil, err
	}
	if err := db.Raw(`SELECT COUNT(*) FROM domains WHERE is_enabled = 1`).Scan(&out.DomainsEnabled).Error; err != nil {
		return nil, err
	}
	if err := db.Raw(`SELECT COUNT(*) FROM users`).Scan(&out.UsersTotal).Error; err != nil {
		return nil, err
	}

	// Mail volume (GH #840). The #873 mail_stats_samples store cumulative
	// counters sampled every 15m; total volume in the window is the sum of
	// positive per-sample deltas (clamped >= 0 so a Stalwart restart's
	// counter reset doesn't subtract). Best-effort: on a mail-less or
	// pre-000248 install the table is absent, so an error here leaves mail
	// stats unavailable (digest omits the line) rather than failing the
	// whole digest.
	collectMailVolume(db, since, out)

	return out, nil
}

// collectMailVolume fills the mail fields best-effort; any error (e.g.
// mail_stats_samples absent) leaves MailStatsAvailable false.
func collectMailVolume(db *gorm.DB, since time.Time, out *DigestStats) {
	var deltas []struct {
		Metric string
		Total  int64
	}
	// SUM of positive deltas per metric over the window. LAG needs the row
	// immediately before each; partitioning per metric + ordering by time
	// gives the previous sample, and GREATEST(...,0) clamps counter resets.
	if err := db.Raw(`SELECT metric, SUM(GREATEST(value - prev, 0)) AS total FROM (
			SELECT metric, value,
			       LAG(value) OVER (PARTITION BY metric ORDER BY sampled_at) AS prev
			FROM mail_stats_samples
			WHERE metric IN ('message_ingest_ham','message_ingest_spam','queue_authenticated_message_queued')
			  AND sampled_at >= ?
		) t WHERE prev IS NOT NULL GROUP BY metric`, since).Scan(&deltas).Error; err != nil {
		return // table absent / query unsupported — leave unavailable
	}
	for _, d := range deltas {
		switch d.Metric {
		case "message_ingest_ham", "message_ingest_spam":
			out.MailReceived += d.Total
		case "queue_authenticated_message_queued":
			out.MailSent += d.Total
		}
	}
	// Current queue depth = latest gauge sample.
	var queueNow int64
	if err := db.Raw(`SELECT value FROM mail_stats_samples
		WHERE metric = 'queue_size' ORDER BY sampled_at DESC LIMIT 1`).Scan(&queueNow).Error; err != nil {
		return
	}
	out.MailQueueNow = queueNow
	out.MailStatsAvailable = true
}
