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
	SFTPAccess bool `gorm:"column:sftp_access;type:tinyint(1);not null" json:"sftp_access"`
	IsEnabled  bool `gorm:"type:tinyint(1);not null" json:"is_enabled"`
	// UID is the subaccount's own uid when isolated (GH #1145 separate-uid
	// model). nil = legacy shared-uid alias sharing the tenant uid. Explicit
	// column tag: GORM mangles the initialism (UID -> u_id) otherwise.
	UID *uint32 `gorm:"column:uid" json:"uid,omitempty"`
	// Isolated selects the security model: true = separate-uid, jailed,
	// kernel-isolated; false = legacy shared-access alias. NO gorm default on
	// purpose — the column default is 0, and a `default:1`-style tag on a bool
	// silently flips an explicit false back to true on create (recurring scar).
	Isolated bool `gorm:"column:isolated;type:tinyint(1);not null" json:"isolated"`
	// QuotaMB is the per-account disk cap (MB) for the split allocation
	// (Σ subaccounts + tenant ≤ package quota). 0 = unset; an isolated account
	// requires quota_mb > 0 — an unlimited separate uid escapes the tenant's
	// own quota and is a disk-fill hole.
	QuotaMB uint32 `gorm:"column:quota_mb;not null" json:"quota_mb"`
	// JailPath is the root-owned jail dir (/var/lib/jabali-ftp-jails/<tenant>/
	// <label>) for isolated accounts; empty for legacy shared-access rows.
	// Stored, not merely derived, so the reconciler can fail a row closed when
	// home_path is not under its jail or the jail has no live mount.
	JailPath  string    `gorm:"column:jail_path;type:varchar(512);not null" json:"jail_path,omitempty"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at,omitempty"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at,omitempty"`
}

func (FtpAccount) TableName() string { return "ftp_accounts" }
