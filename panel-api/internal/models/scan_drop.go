package models

import "time"

// ScanDrop is a scanner feeding a shared Files resource (GH #2503). The
// scanner logs in to its own FTP/SFTP account (FtpAccountID), homed at
// DropPath (/home/<user>/domains/<domain>/scans/<name>); panel-api moves each
// finished file from there into the resource's Stalwart folder.
//
// The row has no foreign keys on purpose: it is the handle the ingest loop
// sweeps by. A row whose resource is gone gets its scanner login removed and
// is then deleted; a row whose login is gone is deleted.
type ScanDrop struct {
	ResourceID   string `gorm:"column:resource_id;type:char(26);primaryKey" json:"resource_id"`
	FtpAccountID string `gorm:"column:ftp_account_id;type:char(26);not null;uniqueIndex:ux_scan_drops_ftp_account" json:"ftp_account_id"`
	DropPath     string `gorm:"column:drop_path;type:varchar(512);not null" json:"drop_path"`
	// MaxMB caps the scans kept in the Stalwart folder; where logins are
	// isolated it is also the login's disk quota.
	MaxMB         uint32     `gorm:"column:max_mb;not null" json:"max_mb"`
	IngestedTotal uint64     `gorm:"column:ingested_total;not null;default:0" json:"ingested_total"`
	LastIngestAt  *time.Time `gorm:"column:last_ingest_at;type:datetime(6)" json:"last_ingest_at,omitempty"`
	LastError     string     `gorm:"column:last_error;type:varchar(512);not null;default:''" json:"last_error"`
	CreatedAt     time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (ScanDrop) TableName() string { return "scan_drops" }
