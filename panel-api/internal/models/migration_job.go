package models

import "time"

// MigrationSourceKind enumerates supported source-panel kinds. Wire
// constants live as string literals so a new importer (e.g. `plesk`)
// only needs a switch arm in internal/migrate, no schema migration.
const (
	MigrationSourceCpanel      = "cpanel"
	MigrationSourceDirectAdmin = "directadmin"
	MigrationSourceHestia      = "hestiacp"
	MigrationSourceWHMpkgacct  = "whm_pkgacct"
	// MigrationSourceWordPressSSH (GH #647) — pull a single WordPress site from
	// any SSH-capable host (Cloudways/VPS/generic). Reuses the SSH pull runner
	// (dispatched by source_kind) with a WP-specific arm: rsync files + wp db
	// export, then the shared migration.import_wp step. No panel-account backup.
	MigrationSourceWordPressSSH = "wordpress_ssh"
	// MigrationSourceWordPressPlugin (GH #648) — PULL a WordPress site from the
	// jabali-migrator plugin's token-authed REST API (no source SSH). Jabali
	// fetches manifest+DB+files, stages, then runs the shared import-wp.
	MigrationSourceWordPressPlugin = "wordpress_plugin"
	// MigrationSourceIMAP (GH #390 Google Workspace / #374 M365) — per-mailbox
	// IMAP account migration. Unlike the panel-account sources above this is
	// per-mailbox: each migrated account carries its own remote IMAP login.
	// The importer FETCHes remote messages into a staging Maildir and hands it
	// to the shared migration.import_mailboxes agent step. See
	// plans/gh374-imap-mail-migration.md.
	MigrationSourceIMAP = "imap"
	// MigrationSourcePlesk (GH #429) — full control-panel migration from a Plesk
	// source over SSH. Discovers subscriptions, WordPress installs, mailboxes,
	// databases, DNS, and (reseller) customers + service plans via `plesk bin` /
	// `plesk ext wp-toolkit` read-only CLI, then restores destination-side by
	// synthesising a cpmove-shaped tarball and reusing the cpanel restore
	// writers. See plans/gh429-plesk-migration.md.
	MigrationSourcePlesk = "plesk"
	// MigrationSourceCloudPanel (GH #522) — full web-hosting migration from a
	// CloudPanel source over SSH. CloudPanel stores its whole inventory in a
	// SQLite database (/home/clp/htdocs/app/data/db.sq3), so discovery reads
	// sites / databases / PHP versions with plain read-only SELECTs; `clpctl`
	// + `mysqldump` handle DB creds + dumps. CloudPanel has NO mail server, so
	// the manifest's Mailboxes area is always empty. Restore reuses the cpanel
	// writers via cpmove synthesis (DirectAdmin strategy). See
	// plans/gh522-cloudpanel-migration.md.
	MigrationSourceCloudPanel = "cloudpanel"
)

// MigrationState is the per-job lifecycle. Stage transitions are
// pinned in internal/migrate/stage.go (Step 2 wave gate).
const (
	// MigrationStateDraft — ADR-0095 decision 5. Wizard creates the
	// row at Step 1 in draft state; PATCH /admin/migrations/:id is
	// allowed in this state only; transition draft → pending flips
	// the runner on at Step 4 submit. Drafts older than 24h are
	// reaped by jabali-migration-secrets-reap.timer.
	MigrationStateDraft      = "draft"
	MigrationStatePending    = "pending"
	MigrationStateAnalyzing  = "analyzing"
	MigrationStateFixPerms   = "fix_perms"
	MigrationStateValidating = "validating"
	MigrationStateRestoring  = "restoring"
	MigrationStateDone       = "done"
	// MigrationStateDegraded (JAB-31): the pipeline ran to completion but a core
	// area failed (all DB dumps skipped, mailbox messages found but none pushed,
	// or 0 healthy domains). Terminal like done/failed but signals "restored,
	// needs attention" so operators don't trust an unusable account. The CLI
	// exits non-zero for this state unless --allow-degraded is passed.
	MigrationStateDegraded  = "degraded"
	MigrationStateFailed    = "failed"
	MigrationStateCancelled = "cancelled"
)

// MigrationJob is the header row for one migration attempt against
// a single (source-host, source-user, source-kind) tuple. Resume
// after a failed mid-run reuses the row — the UNIQUE on the natural
// key prevents two parallel attempts from racing on the same files.
type MigrationJob struct {
	ID string `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	// BatchID — ADR-0095 decision 3. NULL for single-account jobs.
	// Non-NULL ULID groups every job created from one bulk-WHM submit.
	BatchID    *string `gorm:"column:batch_id;type:varchar(26);index" json:"batch_id,omitempty"`
	SourceKind string  `gorm:"column:source_kind;type:varchar(32);not null;uniqueIndex:uq_migration_source,priority:3" json:"source_kind"`
	// Mode (GH #646): "import" (first migration / resume) or "refresh"
	// (force re-pull into an already-migrated account, backup-first).
	Mode            string `gorm:"column:mode;type:varchar(16);not null;default:'import'" json:"mode"`
	SourceHost      string `gorm:"column:source_host;type:varchar(255);not null;uniqueIndex:uq_migration_source,priority:1" json:"source_host"`
	SourceUser      string `gorm:"column:source_user;type:varchar(64);not null;uniqueIndex:uq_migration_source,priority:2" json:"source_user"`
	ExpectedHostKey string `gorm:"column:expected_host_key;type:varchar(128);not null;default:''" json:"expected_host_key"`
	// SourcePort (GH #429) — the source panel's SSH port; connectors default to
	// 22 when unset. Lets operators migrate a source behind a custom SSH port.
	SourcePort int `gorm:"column:source_port;type:int;not null;default:22" json:"source_port"`
	// SourcePath (GH #647) — the WordPress root on the source (control-INPUT,
	// like source_host/source_user). NULL until the operator supplies it or the
	// discoverer auto-detects it. Distinct from ManifestJSON, which holds the
	// discoverer's OUTPUT (home/siteurl/prefix/size). Only wordpress_ssh uses it.
	SourcePath *string `gorm:"column:source_path;type:varchar(1024)" json:"source_path,omitempty"`
	// DestUser/DestDomain (GH #647/#648) — when set, the pull auto-chains to
	// import-wp on success (fire-and-forget background migration).
	DestUser   *string `gorm:"column:dest_user;type:varchar(64)" json:"dest_user,omitempty"`
	DestDomain *string `gorm:"column:dest_domain;type:varchar(255)" json:"dest_domain,omitempty"`
	// PlanJSON (GH #665) — the wizard's plan: selected accounts + import-area
	// toggles. NULL = import everything.
	PlanJSON     *string    `gorm:"column:plan_json;type:text" json:"plan_json,omitempty"`
	TargetUserID *string    `gorm:"column:target_user_id;type:char(26);index:idx_migration_target_user" json:"target_user_id,omitempty"`
	State        string     `gorm:"column:state;type:varchar(32);not null;default:'pending';index:idx_migration_state" json:"state"`
	StartedAt    time.Time  `gorm:"column:started_at;type:datetime(6);not null" json:"started_at"`
	EndedAt      *time.Time `gorm:"column:ended_at;type:datetime(6)" json:"ended_at,omitempty"`
	ManifestJSON *string    `gorm:"column:manifest_json;type:longtext" json:"manifest_json,omitempty"`
	LastError    *string    `gorm:"column:last_error;type:text" json:"last_error,omitempty"`
	CreatedAt    time.Time  `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (MigrationJob) TableName() string { return "migration_jobs" }

// MigrationStage is one row per pipeline stage that runs under a
// MigrationJob. Resume scans for state in {'pending','failed'}
// ordered by created_at and re-dispatches.
type MigrationStage struct {
	ID             string     `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	JobID          string     `gorm:"column:job_id;type:char(26);not null;index:idx_migration_stages_job,priority:1" json:"job_id"`
	StageName      string     `gorm:"column:stage_name;type:varchar(64);not null;index:idx_migration_stages_job,priority:2" json:"stage_name"`
	State          string     `gorm:"column:state;type:varchar(16);not null;default:'pending'" json:"state"`
	StartedAt      *time.Time `gorm:"column:started_at;type:datetime(6)" json:"started_at,omitempty"`
	EndedAt        *time.Time `gorm:"column:ended_at;type:datetime(6)" json:"ended_at,omitempty"`
	BytesProcessed int64      `gorm:"column:bytes_processed;type:bigint;not null;default:0" json:"bytes_processed"`
	LastError      *string    `gorm:"column:last_error;type:text" json:"last_error,omitempty"`
	CreatedAt      time.Time  `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (MigrationStage) TableName() string { return "migration_stages" }
