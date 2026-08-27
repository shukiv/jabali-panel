package repository

// UserResourceStatsRepository batch-computes the "at a glance" per-user counts +
// monthly bandwidth for the admin Users list (GH #1242 follow-up). One GROUP BY
// query per metric over the page's user ids — never N+1 — so a 200-row page costs
// a handful of aggregate reads.

import (
	"context"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// UserResourceStats is the per-user roll-up shown on the admin Users list.
type UserResourceStats struct {
	Domains        int64  `json:"domains"`
	Mailboxes      int64  `json:"mailboxes"`
	Databases      int64  `json:"databases"`
	DockerApps     int64  `json:"docker_apps"`
	Backups        int64  `json:"backups"`
	BandwidthBytes uint64 `json:"bandwidth_bytes"`
}

type UserResourceStatsRepository interface {
	// Fetch returns stats keyed by user id. Users with no rows for a metric
	// simply have zero there; a user with no artifacts at all is absent from the
	// map (the caller treats a missing key as all-zero). monthStart bounds the
	// bandwidth sum to the current billing month.
	Fetch(ctx context.Context, userIDs []string, monthStart time.Time) (map[string]UserResourceStats, error)
}

type userResourceStatsRepo struct{ db *gorm.DB }

func NewUserResourceStatsRepository(db *gorm.DB) UserResourceStatsRepository {
	return &userResourceStatsRepo{db: db}
}

type urStatRow struct {
	UserID string `gorm:"column:user_id"`
	N      int64  `gorm:"column:n"`
}

func (r *userResourceStatsRepo) Fetch(ctx context.Context, userIDs []string, monthStart time.Time) (map[string]UserResourceStats, error) {
	out := make(map[string]UserResourceStats, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	db := r.db.WithContext(ctx)

	assign := func(query string, args []any, set func(*UserResourceStats, int64)) error {
		var rows []urStatRow
		if err := db.Raw(query, args...).Scan(&rows).Error; err != nil {
			return translate(err)
		}
		for _, row := range rows {
			s := out[row.UserID]
			set(&s, row.N)
			out[row.UserID] = s
		}
		return nil
	}

	if err := assign(
		"SELECT user_id, COUNT(*) AS n FROM domains WHERE user_id IN ? GROUP BY user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) { s.Domains = n }); err != nil {
		return nil, err
	}
	if err := assign(
		"SELECT user_id, COUNT(*) AS n FROM databases WHERE user_id IN ? GROUP BY user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) { s.Databases = n }); err != nil {
		return nil, err
	}
	if err := assign(
		"SELECT user_id, COUNT(*) AS n FROM docker_apps WHERE user_id IN ? AND status <> ? GROUP BY user_id",
		[]any{userIDs, models.DockerAppStatusDeleted}, func(s *UserResourceStats, n int64) { s.DockerApps = n }); err != nil {
		return nil, err
	}
	// Mailboxes belong to a domain, which belongs to a user.
	if err := assign(
		"SELECT d.user_id AS user_id, COUNT(*) AS n FROM mailboxes m JOIN domains d ON d.id = m.domain_id WHERE d.user_id IN ? GROUP BY d.user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) { s.Mailboxes = n }); err != nil {
		return nil, err
	}
	// Retained account backups (same filter as CountRetainedForUser).
	if err := assign(
		"SELECT user_id, COUNT(*) AS n FROM backup_jobs WHERE user_id IN ? AND kind = ? AND status IN ? GROUP BY user_id",
		[]any{userIDs, models.BackupJobKindAccountBackup, []string{
			models.BackupJobStatusQueued, models.BackupJobStatusRunning,
			models.BackupJobStatusSucceeded, models.BackupJobStatusPartial,
		}}, func(s *UserResourceStats, n int64) { s.Backups = n }); err != nil {
		return nil, err
	}
	// Monthly bandwidth = SUM of the user's domains' daily bytes since monthStart.
	if err := assign(
		"SELECT d.user_id AS user_id, COALESCE(SUM(b.bytes_total),0) AS n FROM bw_daily b JOIN domains d ON d.id = b.domain_id WHERE d.user_id IN ? AND b.day >= ? GROUP BY d.user_id",
		[]any{userIDs, monthStart}, func(s *UserResourceStats, n int64) {
			if n > 0 {
				s.BandwidthBytes = uint64(n)
			}
		}); err != nil {
		return nil, err
	}
	return out, nil
}
