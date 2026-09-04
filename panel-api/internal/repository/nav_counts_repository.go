package repository

import (
	"context"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// NavCounts holds the resource counts the side-nav badges show (GH #1478).
// Web/Mail/DNS follow the GH #1449 service flags: Web = web-enabled domains,
// Mail = jabali-mail-enabled domains, DNS = domains whose zone the panel hosts
// (a domain has a managed zone iff dns is enabled, so this doubles as the DNS
// Zones count without a join). Backups counts retained account backups only.
type NavCounts struct {
	WebDomains  int64 `json:"web_domains"`
	MailDomains int64 `json:"mail_domains"`
	DNSZones    int64 `json:"dns_zones"`
	Databases   int64 `json:"databases"`
	FTPAccounts int64 `json:"ftp_accounts"`
	Backups     int64 `json:"backups"`
	CronJobs    int64 `json:"cron_jobs"`
}

// NavCountsRepository aggregates the side-nav badge counts. Kept as its own
// small repo (rather than adding methods to seven existing interfaces + their
// mocks) so the counts live in one place and the handler mocks one dependency.
type NavCountsRepository interface {
	// ForUser returns the counts scoped to one owner (tenant nav).
	ForUser(ctx context.Context, userID string) (NavCounts, error)
	// Global returns the fleet-wide counts (admin nav).
	Global(ctx context.Context) (NavCounts, error)
}

type navCountsRepo struct{ db *gorm.DB }

// NewNavCountsRepository builds the badge-count aggregator.
func NewNavCountsRepository(db *gorm.DB) NavCountsRepository { return &navCountsRepo{db: db} }

func (r *navCountsRepo) ForUser(ctx context.Context, userID string) (NavCounts, error) {
	return r.compute(ctx, userID, false)
}

func (r *navCountsRepo) Global(ctx context.Context) (NavCounts, error) {
	return r.compute(ctx, "", true)
}

// compute runs the counts. global=true drops the user_id filter. Each COUNT is
// a cheap indexed aggregate; the three domain counts share one query.
func (r *navCountsRepo) compute(ctx context.Context, userID string, global bool) (NavCounts, error) {
	var out NavCounts
	// user_id scoping applied unless global.
	scope := func(q *gorm.DB) *gorm.DB {
		if global {
			return q
		}
		return q.Where("user_id = ?", userID)
	}

	// Domains: web / mail / dns in a single pass (GH #1449 flags).
	var dom struct {
		Web  int64 `gorm:"column:web"`
		Mail int64 `gorm:"column:mail"`
		DNS  int64 `gorm:"column:dns"`
	}
	if err := scope(r.db.WithContext(ctx).Model(&models.Domain{})).
		Select("COALESCE(SUM(web_disabled = 0),0) AS web, " +
			"COALESCE(SUM(email_enabled = 1),0) AS mail, " +
			"COALESCE(SUM(dns_disabled = 0),0) AS dns").
		Scan(&dom).Error; err != nil {
		return out, translate(err)
	}
	out.WebDomains, out.MailDomains, out.DNSZones = dom.Web, dom.Mail, dom.DNS

	if err := scope(r.db.WithContext(ctx).Model(&models.Database{})).Count(&out.Databases).Error; err != nil {
		return out, translate(err)
	}
	if err := scope(r.db.WithContext(ctx).Model(&models.FtpAccount{})).Count(&out.FTPAccounts).Error; err != nil {
		return out, translate(err)
	}
	if err := scope(r.db.WithContext(ctx).Model(&models.CronJob{})).Count(&out.CronJobs).Error; err != nil {
		return out, translate(err)
	}

	// Backups: retained account backups only (mirrors CountRetainedForUser —
	// exclude failed/deleted job rows so the badge matches the Backups list).
	bq := r.db.WithContext(ctx).Model(&models.BackupJob{}).
		Where("kind = ? AND status IN ?", models.BackupJobKindAccountBackup,
			[]string{
				models.BackupJobStatusQueued, models.BackupJobStatusRunning,
				models.BackupJobStatusSucceeded, models.BackupJobStatusPartial,
			})
	if !global {
		bq = bq.Where("user_id = ?", userID)
	}
	if err := bq.Count(&out.Backups).Error; err != nil {
		return out, translate(err)
	}

	return out, nil
}
