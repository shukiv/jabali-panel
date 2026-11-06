package models

import "time"

// M46 — database server admin ops (ADR-0097..0100).

// DBTuningSetting is one curated config key/value (ADR-0098). The DB is
// the source of truth; the reconciler renders the on-disk drop-in /
// postgresql.auto.conf from these rows and reloads on divergence.
type DBTuningSetting struct {
	ID        string     `gorm:"type:char(26);primaryKey" json:"id"`
	Engine    string     `gorm:"type:varchar(16);not null;uniqueIndex:uniq_db_tuning_engine_param,priority:1" json:"engine"`
	Param     string     `gorm:"type:varchar(64);not null;uniqueIndex:uniq_db_tuning_engine_param,priority:2" json:"param"`
	Value     string     `gorm:"type:varchar(255);not null" json:"value"`
	AppliedAt *time.Time `gorm:"type:datetime(6)" json:"applied_at,omitempty"`
	AppliedBy string     `gorm:"type:char(26)" json:"applied_by,omitempty"`
	CreatedAt time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DBTuningSetting) TableName() string { return "db_tuning_settings" }

// DBAdminJob tracks a long-running maintenance run (ADR-0100). Survives
// panel-api restart so a mid-run page reload doesn't 404 its own job,
// and powers the one-job-per-engine 409 concurrency guard.
type DBAdminJob struct {
	ID          string     `gorm:"type:char(26);primaryKey" json:"id"`
	Engine      string     `gorm:"type:varchar(16);not null" json:"engine"`
	Kind        string     `gorm:"type:varchar(32);not null" json:"kind"`
	Scope       string     `gorm:"type:varchar(64);not null" json:"scope"`
	Status      string     `gorm:"type:varchar(16);not null" json:"status"` // running|ok|error
	Summary     string     `gorm:"type:text" json:"summary,omitempty"`
	ActorUserID string     `gorm:"type:char(26);not null" json:"actor_user_id"`
	StartedAt   time.Time  `gorm:"type:datetime(6);not null" json:"started_at"`
	FinishedAt  *time.Time `gorm:"type:datetime(6)" json:"finished_at,omitempty"`

	// RunningEngine is Engine while the job runs and NULL once it finishes.
	// A unique key on it lets the insert refuse a second running job for an
	// engine. The repository sets and clears it.
	RunningEngine *string `gorm:"type:varchar(16)" json:"-"`
}

func (DBAdminJob) TableName() string { return "db_admin_jobs" }

// DBAdminAudit is a tamper-evident row per privileged DB admin action
// (ADR-0097..0100). detail NEVER contains secrets.
type DBAdminAudit struct {
	ID          string    `gorm:"type:char(26);primaryKey" json:"id"`
	TS          time.Time `gorm:"column:ts;type:datetime(6);not null" json:"ts"`
	ActorUserID string    `gorm:"type:char(26);not null" json:"actor_user_id"`
	Engine      string    `gorm:"type:varchar(16);not null" json:"engine"`
	Action      string    `gorm:"type:varchar(64);not null" json:"action"`
	Target      string    `gorm:"type:varchar(255);not null;default:''" json:"target"`
	Outcome     string    `gorm:"type:varchar(32);not null" json:"outcome"`
	Detail      string    `gorm:"type:varchar(255);not null;default:''" json:"detail"`
}

func (DBAdminAudit) TableName() string { return "db_admin_audit" }
