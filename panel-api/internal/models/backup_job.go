// M30 backup-restore foundation. backup_jobs is the audit/workflow row
// behind every backup or restore run; bundle bytes live in restic.
// Schema lands in migration 000084; constants here mirror the ENUMs.
// See ADR-0075 + plans/m30-backup-restore.md.
package models

import (
	"encoding/json"
	"time"
)

// BackupJobKind discriminates the four operator-visible workflows.
// Renamed from the parked-plan triplet (backup/restore/download) once
// system-level backup/restore got first-class roles in M30 Steps 10-11.
const (
	BackupJobKindAccountBackup  = "account_backup"
	BackupJobKindAccountRestore = "account_restore"
	BackupJobKindSystemBackup   = "system_backup"
	BackupJobKindSystemRestore  = "system_restore"
)

// BackupJobStatus mirrors the ENUM in migration 000084. `partial` covers
// the multi-stage case where a non-fatal stage failure left the rest of
// the bundle intact (manifest_json.warnings carries the detail).
const (
	BackupJobStatusQueued    = "queued"
	BackupJobStatusRunning   = "running"
	BackupJobStatusSucceeded = "succeeded"
	BackupJobStatusPartial   = "partial"
	BackupJobStatusFailed    = "failed"
	BackupJobStatusCancelled = "cancelled"
)

// BackupJob is the workflow row for one backup or restore run. The
// restic snapshot ID lands in SnapshotID once the orchestrator seals
// the manifest snapshot; ParentSnapshot tracks the incremental parent
// for dedup-win reporting.
//
// The model intentionally omits any `gorm:"index:..."` directives — the
// indexes are created in the migration so the schema and the code stay
// readable side-by-side (other M-series tables follow the same pattern).
type BackupJob struct {
	ID            string  `gorm:"type:char(26);primaryKey"                                                  json:"id"`
	UserID        string  `gorm:"type:char(26);not null"                                                    json:"user_id"`
	DestinationID *string `gorm:"type:char(26)"                                                             json:"destination_id,omitempty"`
	ScheduleID    *string `gorm:"type:char(26)"                                                             json:"schedule_id,omitempty"`
	RunID         *string `gorm:"type:char(26)"                                                             json:"run_id,omitempty"`
	Kind          string  `gorm:"type:enum('account_backup','account_restore','system_backup','system_restore');not null" json:"kind"`
	// Content records what this run captured: full / files / database / folders
	// (GH #294 selectors, GH #454). Denormalised from the schedule at enqueue
	// time so the dispatcher and the history UI both read it off the job without
	// re-loading the schedule (which may have changed or been deleted since).
	Content        string          `gorm:"column:content;type:varchar(16);not null;default:'full'"                   json:"content"`
	Status         string          `gorm:"type:enum('queued','running','succeeded','partial','failed','cancelled');not null;default:'queued'" json:"status"`
	SystemdUnit    string          `gorm:"type:varchar(128);not null"                                                json:"systemd_unit"`
	SnapshotID     string          `gorm:"type:char(64);not null;default:''"                                         json:"snapshot_id"`
	ParentSnapshot string          `gorm:"type:char(64);not null;default:''"                                         json:"parent_snapshot"`
	BytesAdded     uint64          `gorm:"type:bigint unsigned;not null;default:0"                                   json:"bytes_added"`
	BytesTotal     uint64          `gorm:"type:bigint unsigned;not null;default:0"                                   json:"bytes_total"`
	ManifestJSON   json.RawMessage `gorm:"type:json"                                                                 json:"manifest_json,omitempty"`
	WarningsJSON   json.RawMessage `gorm:"type:json"                                                                 json:"warnings_json,omitempty"`
	ErrorText      string          `gorm:"type:text"                                                                 json:"error_text,omitempty"`
	SourceHostname string          `gorm:"type:varchar(253);not null;default:''"                                     json:"source_hostname"`
	SourcePanelSHA string          `gorm:"column:source_panel_sha;type:char(40);not null;default:''"                 json:"source_panel_sha"`
	CreatedAt      time.Time       `gorm:"type:datetime(6);not null"                                                 json:"created_at"`
	StartedAt      *time.Time      `gorm:"type:datetime(6)"                                                          json:"started_at,omitempty"`
	FinishedAt     *time.Time      `gorm:"type:datetime(6)"                                                          json:"finished_at,omitempty"`
}

// TableName pins to the exact table from migration 000084.
func (BackupJob) TableName() string { return "backup_jobs" }
