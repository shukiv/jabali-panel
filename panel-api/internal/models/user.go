// Package models holds the GORM struct definitions that mirror the panel's
// own database schema (the `jabali_panel` DB on MariaDB). Customer-hosted
// databases are a separate concern and are NOT modelled here.
package models

import (
	"time"
)

// User is a panel account. Two personas share the table:
//
//   - Admin users: panel operators (is_admin=true). Managed by the
//     seed/bootstrap flow; is_admin is never settable via the public API.
//   - Hosting users: panel customers (is_admin=false) mapped to a Linux UID
//     once the agent provisions the OS user; linux_uid is NULL until then.
type User struct {
	// ID is a 26-char ULID (Crockford base32). See internal/ids.
	ID string `gorm:"type:char(26);primaryKey" json:"id"`

	Email string `gorm:"type:varchar(255);not null" json:"email"` // M54: no longer unique (may be shared)

	// Username is the Linux account name for this user. NULL for admins
	// (panel-only, no OS account). Must match POSIX regex
	// ^[a-z_][a-z0-9_-]{0,31}$. Unique across the panel.
	Username *string `gorm:"type:varchar(32);uniqueIndex:ux_users_username" json:"username,omitempty"`
	// CLIPHPVersion is the user's chosen default PHP version for shell/CLI
	// (bare `php`, composer, wp-cli). NULL = auto (follow the pool pin). GH #256.
	CLIPHPVersion *string `gorm:"column:cli_php_version;type:varchar(8)" json:"cli_php_version,omitempty"`
	// ComposerChannel is the user's chosen Composer version channel for the
	// shell `composer` (GH #1332 item 13). NULL/"latest" = the latest Composer;
	// "lts" = the 2.2 LTS. Applied by the host dispatcher via a choice file.
	ComposerChannel *string `gorm:"column:composer_channel;type:varchar(16)" json:"composer_channel,omitempty"`

	NameFirst    string `gorm:"type:varchar(100);not null;default:''"                 json:"name_first"`
	NameLast     string `gorm:"type:varchar(100);not null;default:''"                 json:"name_last"`
	PasswordHash string `gorm:"type:varchar(255);not null"                            json:"-"`

	// IsAdmin is stored as TINYINT(1). Never written from a request DTO —
	// only by an admin-only handler (Phase 6) or the bootstrap seed.
	IsAdmin bool `gorm:"type:tinyint(1);not null;default:0" json:"is_admin"`

	// PackageID links to hosting_packages. NULL for admin-only accounts.
	PackageID *string `gorm:"type:char(26)" json:"package_id,omitempty"`

	// LinuxUID is set by the handler after the agent successfully creates
	// the OS user. NULL until then, or for admin-only accounts.
	LinuxUID *uint32 `gorm:"type:int unsigned" json:"linux_uid,omitempty"`

	// MySQL Admin Shadow Account fields for phpMyAdmin SSO.
	// MysqladminUsername is the shadow MariaDB user created for SSO.
	// NULL until first SSO provision.
	MysqladminUsername *string `gorm:"type:varchar(64)" json:"mysqladmin_username,omitempty"`

	// MysqladminPasswordEnc is the AES-256-GCM encrypted password for the
	// shadow account. Never exported in API responses. NULL until first SSO provision.
	MysqladminPasswordEnc []byte `gorm:"type:varbinary(512)" json:"-"`

	// MysqladminProvisionedAt is the timestamp of the first SSO provision.
	// NULL until the shadow account is created.
	MysqladminProvisionedAt *time.Time `gorm:"type:datetime(6)" json:"mysqladmin_provisioned_at,omitempty"`

	// PgadminUsername is the shadow PostgreSQL ROLE created for the
	// Adminer SSO bridge (M37 Phase 4). Mirrors mysqladmin_username
	// but for engine='postgres' rows. NULL until first provision.
	PgadminUsername *string `gorm:"type:varchar(64)" json:"pgadmin_username,omitempty"`

	// PgadminPasswordEnc is the AES-256-GCM encrypted password for
	// the PG shadow ROLE. Same SSO key as mysqladmin_password_enc.
	PgadminPasswordEnc []byte `gorm:"type:varbinary(512)" json:"-"`

	// PgadminProvisionedAt is the timestamp of the first PG SSO provision.
	PgadminProvisionedAt *time.Time `gorm:"type:datetime(6)" json:"pgadmin_provisioned_at,omitempty"`

	// KratosIdentityID is the UUID of the corresponding Kratos identity.
	// Set atomically during user creation (inline hook) or during kratos-migrate batch.
	// NULL only for rows created before the identity write landed — should
	// not happen on a post-M20 install, but the column stays nullable so a
	// compensating-delete race can't FK-fail the panel insert.
	KratosIdentityID *string `gorm:"type:varchar(64);uniqueIndex:ux_users_kratos_identity_id" json:"kratos_identity_id,omitempty"`

	// Suspended marks a user as administratively offline (migration
	// 000132). Admin suspend POSTs flip the flag + push Kratos identity
	// state=inactive (blocks panel + webmail login) + flip every owned
	// domains.is_enabled=0 (reconciler drops the nginx symlink so sites
	// serve 404). SuspendedAt + SuspendReason are operator-facing audit
	// fields surfaced in the admin UI.
	Suspended     bool       `gorm:"column:suspended;type:tinyint(1);not null;default:0;index:idx_users_suspended" json:"suspended"`
	SuspendedAt   *time.Time `gorm:"column:suspended_at;type:datetime(6)"                                            json:"suspended_at,omitempty"`
	SuspendReason string     `gorm:"column:suspend_reason;type:varchar(255);not null;default:''"                     json:"suspend_reason"`

	// WebmailEnabled (GH #316) gates the Bulwark webmail vhost for ALL of this
	// user's domains. AND-ed with global server_settings.webmail_enabled and the
	// per-domain domains.webmail_enabled flags by the webmail reconciler. Default
	// 1 = ON (migration 000203). Mail delivery (IMAP/SMTP/JMAP) is unaffected.
	WebmailEnabled bool `gorm:"column:webmail_enabled;type:tinyint(1);not null;default:1" json:"webmail_enabled"`

	// SSHForwardingEnabled (GH #1229) opts an SSH-enabled user out of the
	// JAB-352 forwarding lockdown into loopback-only TCP forwarding, which
	// VS Code Remote-SSH needs to reach its own VS Code Server on 127.0.0.1.
	// Default 0 = OFF: the lockdown is unchanged for everyone. Admin-only to
	// flip. The SSH reconciler converges jabali-ssh-forward group membership
	// from this flag, so the opt-in is durable across user reprovision (unlike
	// the v1 group-only opt-in, which reverted to OFF). Sensitive loopback
	// services stay firewall-blocked per-uid regardless, so an opted-in user
	// can still only forward to their own apps + the VS Code Server.
	SSHForwardingEnabled bool `gorm:"column:ssh_forwarding_enabled;type:tinyint(1);not null;default:0" json:"ssh_forwarding_enabled"`

	// Disk-usage snapshot, written by the disk-usage sweeper
	// (panel-api/internal/diskusagesweeper) from the same agent report the
	// per-user /users/:id/usage endpoint calls.
	//
	// It lives on the row so the admin Users list can ORDER BY it: sorting
	// is server-side against a column whitelist, and a value that only
	// exists behind a per-row API call cannot be sorted at all.
	//
	// DiskCheckedAt NULL = never swept. The UI falls back to the per-row
	// fetch for those rows, so a freshly-upgraded panel still shows numbers
	// before the first sweep completes.
	DiskUsedKB    uint64     `gorm:"column:disk_used_kb;type:bigint unsigned;not null;default:0"  json:"disk_used_kb"`
	DiskLimitKB   uint64     `gorm:"column:disk_limit_kb;type:bigint unsigned;not null;default:0" json:"disk_limit_kb"`
	DiskCheckedAt *time.Time `gorm:"column:disk_checked_at;type:datetime(6)"                      json:"disk_checked_at,omitempty"`

	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

// TableName pins the plural form in case GORM ever changes its default.
func (User) TableName() string { return "users" }
