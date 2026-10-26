package models

import (
	"strings"
	"time"
)

// ComposerRun is one Composer run a user started from the panel for one of
// their domains (GH #2276, ADR-0176). The agent runs it as
// jabali-composer-<ID>.service; the panel fills in the result once it has
// collected it from the agent.
type ComposerRun struct {
	ID       string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID   string `gorm:"type:char(26);not null" json:"user_id"`
	DomainID string `gorm:"type:char(26);not null;index:idx_composer_runs_domain_created,priority:1" json:"domain_id"`
	// AppDir is the directory Composer ran in, relative to the domain's
	// directory (/home/<user>/domains/<domain>); "" is that directory.
	AppDir  string `gorm:"column:app_dir;type:varchar(255);not null" json:"app_dir"`
	Command string `gorm:"type:varchar(32);not null" json:"command"`
	// Packages holds the run's packages one per line; see PackageList.
	Packages   string `gorm:"type:text" json:"-"`
	NoDev      bool   `gorm:"column:no_dev;not null" json:"no_dev"`
	PHPVersion string `gorm:"column:php_version;type:varchar(8);not null" json:"php_version"`
	State      string `gorm:"type:varchar(16);not null;index:idx_composer_runs_state" json:"state"`
	// ExitCode is Composer's exit code; nil while running, or when the run was
	// killed or lost before it exited.
	ExitCode *int `gorm:"column:exit_code" json:"exit_code"`
	// Result is systemd's Result for the run's unit (success, exit-code,
	// timeout, oom-kill, ...).
	Result            string `gorm:"type:varchar(32);not null" json:"result"`
	PHPDefenseBlocked bool   `gorm:"column:php_defense_blocked;not null" json:"php_defense_blocked"`
	// Log is the end of the run's output, at most 256 KiB, stored once the
	// run has finished.
	Log          string `gorm:"type:mediumtext" json:"-"`
	LogTruncated bool   `gorm:"column:log_truncated;not null" json:"log_truncated"`
	// Error says why a run never started, or was lost.
	Error      string     `gorm:"type:varchar(1024);not null" json:"error"`
	StartedAt  time.Time  `gorm:"type:datetime(6);not null" json:"started_at"`
	FinishedAt *time.Time `gorm:"type:datetime(6)" json:"finished_at"`
	CreatedAt  time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt  time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`
}

// Composer run states.
const (
	ComposerRunRunning   = "running"
	ComposerRunSucceeded = "succeeded"
	ComposerRunFailed    = "failed"
	ComposerRunTimedOut  = "timed_out"
	ComposerRunCancelled = "cancelled"
)

func (ComposerRun) TableName() string { return "composer_runs" }

// PackageList is the run's packages.
func (r ComposerRun) PackageList() []string {
	if r.Packages == "" {
		return []string{}
	}
	return strings.Split(r.Packages, "\n")
}

// SetPackageList stores the run's packages. A package never holds a newline:
// the API accepts only Composer package names and version constraints.
func (r *ComposerRun) SetPackageList(pkgs []string) {
	r.Packages = strings.Join(pkgs, "\n")
}
