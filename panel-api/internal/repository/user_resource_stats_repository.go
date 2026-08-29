package repository

// UserResourceStatsRepository batch-computes the "at a glance" per-user counts +
// monthly bandwidth for the admin Users list (GH #1242 follow-up). One GROUP BY
// query per metric over the page's user ids — never N+1 — so a 200-row page costs
// a handful of aggregate reads.

import (
	"context"
	"errors"
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
	// DBBytes + MailBytes are the tenant's database + mailbox storage (GH #1242),
	// so the Users list can show total usage = home (users.disk_used_kb) + DB +
	// mail. Refreshed by the DB-usage + mailbox-usage sweepers.
	DBBytes   uint64 `json:"db_bytes"`
	MailBytes uint64 `json:"mail_bytes"`
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

	// Each metric is independent: a single failing query must NOT zero every
	// count (GH #1242 — `databases` is a MariaDB reserved word and the unquoted
	// raw query 1064'd, which aborted the whole Fetch and rendered the entire
	// Resources column as 0s). Collect per-query errors and return the partial
	// map so the caller still shows what succeeded. Table names are backticked —
	// `databases` in particular is reserved.
	var errs []error
	assign := func(query string, args []any, set func(*UserResourceStats, int64)) {
		var rows []urStatRow
		if err := db.Raw(query, args...).Scan(&rows).Error; err != nil {
			errs = append(errs, translate(err))
			return
		}
		for _, row := range rows {
			s := out[row.UserID]
			set(&s, row.N)
			out[row.UserID] = s
		}
	}

	assign(
		"SELECT user_id, COUNT(*) AS n FROM `domains` WHERE user_id IN ? GROUP BY user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) { s.Domains = n })
	assign(
		"SELECT user_id, COUNT(*) AS n FROM `databases` WHERE user_id IN ? GROUP BY user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) { s.Databases = n })
	assign(
		"SELECT user_id, COUNT(*) AS n FROM `docker_apps` WHERE user_id IN ? AND status <> ? GROUP BY user_id",
		[]any{userIDs, models.DockerAppStatusDeleted}, func(s *UserResourceStats, n int64) { s.DockerApps = n })
	// Mailboxes belong to a domain, which belongs to a user.
	assign(
		"SELECT d.user_id AS user_id, COUNT(*) AS n FROM `mailboxes` m JOIN `domains` d ON d.id = m.domain_id WHERE d.user_id IN ? GROUP BY d.user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) { s.Mailboxes = n })
	// Retained account backups (same filter as CountRetainedForUser).
	assign(
		"SELECT user_id, COUNT(*) AS n FROM `backup_jobs` WHERE user_id IN ? AND kind = ? AND status IN ? GROUP BY user_id",
		[]any{userIDs, models.BackupJobKindAccountBackup, []string{
			models.BackupJobStatusQueued, models.BackupJobStatusRunning,
			models.BackupJobStatusSucceeded, models.BackupJobStatusPartial,
		}}, func(s *UserResourceStats, n int64) { s.Backups = n })
	// Monthly bandwidth = SUM of the user's domains' daily bytes since monthStart.
	assign(
		"SELECT d.user_id AS user_id, COALESCE(SUM(b.bytes_total),0) AS n FROM `bw_daily` b JOIN `domains` d ON d.id = b.domain_id WHERE d.user_id IN ? AND b.day >= ? GROUP BY d.user_id",
		[]any{userIDs, monthStart}, func(s *UserResourceStats, n int64) {
			if n > 0 {
				s.BandwidthBytes = uint64(n)
			}
		})
	// DB storage = SUM of the user's tenant DB sizes (swept into size_bytes).
	assign(
		"SELECT user_id, COALESCE(SUM(size_bytes),0) AS n FROM `databases` WHERE user_id IN ? GROUP BY user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) {
			if n > 0 {
				s.DBBytes = uint64(n)
			}
		})
	// Mail storage = SUM of the user's mailbox usage (swept into last_usage_bytes).
	assign(
		"SELECT d.user_id AS user_id, COALESCE(SUM(m.last_usage_bytes),0) AS n FROM `mailboxes` m JOIN `domains` d ON d.id = m.domain_id WHERE d.user_id IN ? GROUP BY d.user_id",
		[]any{userIDs}, func(s *UserResourceStats, n int64) {
			if n > 0 {
				s.MailBytes = uint64(n)
			}
		})

	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}
