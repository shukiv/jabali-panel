package models

import "time"

// DomainGitDeployment is one Git deployment, or rollback, of a domain (GH
// #2277, ADR-0178). The agent runs it as jabali-gitdeploy-<ID>.service, as
// the domain's owner; the panel fills in the result once it has collected it
// from the agent.
type DomainGitDeployment struct {
	ID       string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID   string `gorm:"type:char(26);not null" json:"user_id"`
	DomainID string `gorm:"type:char(26);not null;index:idx_domain_git_deployments_domain_created,priority:1" json:"domain_id"`
	// OSUser is the system account the run ran as. Its credential file is in
	// that account's home until the panel releases the run, whoever owns the
	// domain by then.
	OSUser string `gorm:"column:os_user;type:varchar(32);not null" json:"-"`
	// Kind is deploy or rollback.
	Kind string `gorm:"type:varchar(16);not null" json:"kind"`
	// ReleaseID is the release the run makes live: the run's own ID for a
	// deployment, an earlier deployment's for a rollback.
	ReleaseID string `gorm:"column:release_id;type:char(26);not null" json:"release_id"`
	Branch    string `gorm:"type:varchar(255);not null" json:"branch"`
	// CommitSHA is the commit a deployment fetched, once the agent has
	// reported it.
	CommitSHA string `gorm:"column:commit_sha;type:varchar(64);not null" json:"commit_sha"`
	// DocRoot is the document root the domain gets once the run has
	// succeeded: /home/<user>/domains/<domain>/current[/<web_dir>].
	DocRoot    string `gorm:"column:doc_root;type:varchar(512);not null" json:"doc_root"`
	PHPVersion string `gorm:"column:php_version;type:varchar(8);not null" json:"php_version"`
	State      string `gorm:"type:varchar(16);not null;index:idx_domain_git_deployments_state" json:"state"`
	// ExitCode is the run's exit code; nil while running, or when the run
	// was killed or lost before it exited.
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

// Git deployment kinds.
const (
	GitDeployKindDeploy   = "deploy"
	GitDeployKindRollback = "rollback"
)

// Git deployment states.
const (
	GitDeployRunning   = "running"
	GitDeploySucceeded = "succeeded"
	GitDeployFailed    = "failed"
	GitDeployTimedOut  = "timed_out"
	GitDeployCancelled = "cancelled"
)

func (DomainGitDeployment) TableName() string { return "domain_git_deployments" }
