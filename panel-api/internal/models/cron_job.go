package models

import "time"

// CronJob represents a user-scheduled cron job.
type CronJob struct {
	ID       string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID   string `gorm:"type:char(26);not null" json:"user_id"`
	Name     string `gorm:"type:varchar(100);not null" json:"name"`
	Command  string `gorm:"type:varchar(1024);not null" json:"command"`
	Schedule string `gorm:"type:varchar(100);not null" json:"schedule"`
	// No gorm `default:1` tag: GORM substitutes a defaulted field's
	// default for a zero value EVEN under Select, so `default:1` would
	// flip an intentional Enabled=false to enabled=1 on insert (the
	// cPanel cron importer's disabled-import was defeated this way). The
	// DB column keeps its `DEFAULT 1` from the migration for non-app
	// inserts; the repo Create selects `enabled` explicitly so the
	// struct value is always written.
	Enabled bool `gorm:"type:tinyint(1);not null" json:"enabled"`
	// RunAsRoot is admin-only (gated in panel-api/internal/api/cron.go).
	// A root job must be owned by an admin account (cronops enforces this
	// on Create and Update — ErrRootOwnerNotAdmin). When true, the agent writes a SYSTEM-scoped systemd timer at
	// /etc/systemd/system/jabali-cron-<id>.timer that runs the command
	// as uid 0 -- bypassing the per-user systemd dispatch path.
	RunAsRoot    bool       `gorm:"column:run_as_root;type:tinyint(1);not null;default:0" json:"run_as_root"`
	LastRunAt    *time.Time `gorm:"type:timestamp;null" json:"last_run_at"`
	LastExitCode *int       `gorm:"type:int;null" json:"last_exit_code"`
	LastError    *string    `gorm:"type:varchar(1024);null" json:"last_error"`
	CreatedAt    time.Time  `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP;onUpdate:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (CronJob) TableName() string { return "cron_jobs" }
