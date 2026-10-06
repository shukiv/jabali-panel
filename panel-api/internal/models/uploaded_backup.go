package models

import "time"

// UploadedBackup (GH #1993) is an account backup an admin uploaded from
// another server. The archive stays on this server (uploadedbackups.Path
// derives its file from ID) and is listed in Backups, from where it can be
// restored again or deleted.
type UploadedBackup struct {
	ID              string     `gorm:"type:char(26);primaryKey" json:"id"`
	FileName        string     `gorm:"type:varchar(255);not null;default:''" json:"file_name"`
	SizeBytes       int64      `gorm:"type:bigint unsigned;not null;default:0" json:"size_bytes"`
	AccountUsername string     `gorm:"type:varchar(64);not null;default:''" json:"account_username"`
	AccountEmail    string     `gorm:"type:varchar(255);not null;default:''" json:"account_email"`
	Components      string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	Retention       string     `gorm:"type:varchar(24);not null;default:'keep'" json:"retention"`
	ExpiresAt       *time.Time `gorm:"type:datetime(6)" json:"expires_at"`
	UploadedBy      string     `gorm:"type:char(26);not null;default:''" json:"uploaded_by"`
	RestoreStatus   string     `gorm:"type:varchar(16);not null;default:''" json:"restore_status"`
	// RestoreStartedAt is when the running or last restore started.
	RestoreStartedAt *time.Time `gorm:"type:datetime(6)" json:"restore_started_at"`
	RestoredAt       *time.Time `gorm:"type:datetime(6)" json:"restored_at"`
	RestoreTarget    string     `gorm:"type:varchar(64);not null;default:''" json:"restore_target"`
	// RestoreResult is the last restore's report as JSON (applied, warnings,
	// error); the API decodes it.
	RestoreResult *string   `gorm:"type:text" json:"-"`
	CreatedAt     time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt     time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

// TableName pins the table name.
func (UploadedBackup) TableName() string { return "uploaded_backups" }

// Uploaded backup retention choices.
const (
	UploadedBackupKeep               = "keep"
	UploadedBackupKeep7Days          = "keep_7_days"
	UploadedBackupDeleteAfterRestore = "delete_after_restore"
)

// Uploaded backup restore statuses.
const (
	UploadedBackupRestoring = "restoring"
	UploadedBackupDone      = "done"
	UploadedBackupFailed    = "failed"
)
