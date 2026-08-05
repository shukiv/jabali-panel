// M30.1 backup schedules (ADR-0078). Schema in migration 000087/088.
// One row per (kind, user_id, cron_expr) tuple. user_id NULL is
// reserved for system_backup (server-wide). Multi-cadence per user
// = multiple rows; restic dedup makes the incremental cost trivial.
package models

import "time"

const (
	BackupScheduleKindAccount = "account_backup"
	BackupScheduleKindSystem  = "system_backup"
)

type BackupSchedule struct {
	ID     string  `gorm:"type:char(26);primaryKey" json:"id"`
	Kind   string  `gorm:"type:enum('account_backup','system_backup');not null" json:"kind"`
	UserID *string `gorm:"type:char(26)" json:"user_id,omitempty"`
	// IncludeSystemBackup, when true on a kind=account_backup schedule,
	// fires a system.backup job alongside the per-user account fan-out
	// every time the schedule ticks. Ignored on kind=system_backup
	// (those always back up the system by definition).
	IncludeSystemBackup bool   `gorm:"not null;default:0" json:"include_system_backup"`
	CronExpr            string `gorm:"type:varchar(64);not null" json:"cron_expr"`
	// Content is what a firing of this schedule backs up: full / files /
	// database / folders (GH #454). Default 'full' preserves the pre-feature
	// behaviour. Used by the scheduler fan-out (wired in a follow-up) and set by
	// tenants on their own schedule.
	Content string `gorm:"column:content;type:varchar(16);not null;default:'full'" json:"content"`
	// Cadence (GH #454 7B) is the tenant's chosen firing cadence for a
	// window-governed schedule (hourly / every_6h / every_12h / daily / weekly).
	// Empty '' = a legacy cron-governed schedule (admin fan-out, system, or the
	// pre-7B single tenant schedule) that keeps using CronExpr + NextRunAt. A
	// non-empty cadence routes the schedule through the window-guarded dispatch
	// path (internal/backup.NextTenantRun), where CronExpr is ignored.
	Cadence     string     `gorm:"column:cadence;type:varchar(16);not null;default:''" json:"cadence"`
	Enabled     bool       `gorm:"not null;default:1" json:"enabled"`
	KeepDaily   *int       `gorm:"type:int" json:"keep_daily,omitempty"`
	KeepWeekly  *int       `gorm:"type:int" json:"keep_weekly,omitempty"`
	KeepMonthly *int       `gorm:"type:int" json:"keep_monthly,omitempty"`
	LastRunAt   *time.Time `gorm:"type:datetime(6)" json:"last_run_at,omitempty"`
	NextRunAt   *time.Time `gorm:"type:datetime(6)" json:"next_run_at,omitempty"`
	CreatedAt   time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`

	// Loaded by repository when ListWithDestinations is called; not a
	// GORM relation tag (the join table has its own model below).
	Destinations []BackupDestination `gorm:"-" json:"destinations,omitempty"`
	// UserIDs is the explicit per-schedule user list (multi-select on
	// account schedules). Empty = "all non-admin users" fan-out at
	// tick time. Loaded via repository.GetUsers; the legacy single
	// user_id column on this table is ignored once UserIDs is loaded.
	UserIDs []string `gorm:"-" json:"user_ids,omitempty"`
}

// BackupScheduleUser is the M:N join row pairing schedules to users
// for account_backup fan-out.
type BackupScheduleUser struct {
	ScheduleID string    `gorm:"type:char(26);primaryKey" json:"schedule_id"`
	UserID     string    `gorm:"type:char(26);primaryKey" json:"user_id"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (BackupScheduleUser) TableName() string { return "backup_schedule_users" }

func (BackupSchedule) TableName() string { return "backup_schedules" }

// BackupScheduleDestination is the M:N join row. Repositories set
// CreatedAt explicitly; GORM does not auto-stamp on plain join tables.
type BackupScheduleDestination struct {
	ScheduleID    string    `gorm:"type:char(26);primaryKey" json:"schedule_id"`
	DestinationID string    `gorm:"type:char(26);primaryKey" json:"destination_id"`
	CreatedAt     time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (BackupScheduleDestination) TableName() string {
	return "backup_schedule_destinations"
}
