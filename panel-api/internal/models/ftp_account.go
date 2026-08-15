package models

import "time"

// FtpAccount is a tenant-created file-transfer subaccount (GH #1053,
// plans/gh1053-ftp-accounts.md). The agent realizes each row as a REAL
// system user sharing the owning tenant's uid/gid (useradd --non-unique),
// with its own username, password, and home directory — so files written
// over SFTP or FTPS are owned by the tenant uid and quotas / per-user FPM /
// AppArmor treat them exactly like tenant-created files.
//
// There is deliberately NO password field: credentials exist only in
// /etc/shadow, written by the agent at create / password-reset time. The
// panel never stores or returns them.
type FtpAccount struct {
	ID     string `gorm:"type:char(26);primaryKey" json:"id,omitempty"`
	UserID string `gorm:"type:char(26);not null;index" json:"user_id"`
	// Username is the full system username, namespaced under the owning
	// tenant (`<tenant>_<label>`) so it can never collide with a real user
	// or another tenant's accounts. Validated at the API boundary; the
	// agent re-validates before touching passwd.
	Username string `gorm:"type:varchar(64);not null;uniqueIndex" json:"username"`
	// HomePath is absolute and must resolve inside the owning tenant's
	// home (symlink-resolved server-side — path escape = cross-tenant
	// read). Typically a docroot.
	HomePath string `gorm:"type:varchar(512);not null" json:"home_path"`
	// FTPAccess gates membership in the jabali-ftp PAM group. Effective
	// only while server_settings.ftp_enabled is on.
	FTPAccess bool `gorm:"column:ftp_access;type:tinyint(1);not null" json:"ftp_access"`
	// SFTPAccess gates the sshd Match block. Column default is 1; the tag
	// carries NO gorm default on purpose — `default:1` on a bool silently
	// flips an explicit false back to true on create (recurring scar).
	SFTPAccess bool      `gorm:"column:sftp_access;type:tinyint(1);not null" json:"sftp_access"`
	IsEnabled  bool      `gorm:"type:tinyint(1);not null" json:"is_enabled"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at,omitempty"`
	UpdatedAt  time.Time `gorm:"type:datetime(6);not null" json:"updated_at,omitempty"`
}

func (FtpAccount) TableName() string { return "ftp_accounts" }
