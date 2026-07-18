package models

import "time"

// CacheWarmupRun records one cache-warmup pass so the panel can show status
// (state, warmed/attempted/failed, first error) and, in a later phase, resume
// an interrupted run from CursorPos. (JAB-95 Phase 3)
type CacheWarmupRun struct {
	ID         string     `gorm:"type:char(26);primaryKey" json:"id"`
	InstallID  string     `gorm:"type:char(26);not null;index:idx_cwr_install_created,priority:1" json:"install_id"`
	Host       string     `gorm:"type:varchar(255);not null" json:"host"`
	State      string     `gorm:"type:varchar(16);not null;default:queued" json:"state"`
	Requested  int        `gorm:"not null;default:0" json:"requested"`
	ClampedTo  int        `gorm:"column:clamped_to;not null;default:0" json:"clamped_to"`
	Total      int        `gorm:"not null;default:0" json:"total"`
	CursorPos  int        `gorm:"column:cursor_pos;not null;default:0" json:"cursor_pos"`
	Attempted  int        `gorm:"not null;default:0" json:"attempted"`
	Warmed     int        `gorm:"not null;default:0" json:"warmed"`
	Skipped    int        `gorm:"not null;default:0" json:"skipped"`
	Failed     int        `gorm:"not null;default:0" json:"failed"`
	FirstError string     `gorm:"column:first_error;type:varchar(1024);not null;default:''" json:"first_error"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Warmup run states.
const (
	CacheWarmupStateQueued  = "queued"
	CacheWarmupStateRunning = "running"
	CacheWarmupStateDone    = "done"
	CacheWarmupStateFailed  = "failed"
)

func (CacheWarmupRun) TableName() string { return "cache_warmup_runs" }
