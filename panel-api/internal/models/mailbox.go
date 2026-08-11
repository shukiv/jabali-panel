package models

import "time"

// Mailbox is a per-domain email account. Stalwart's SqlDirectory reads
// this table on every auth (ADR-0042); the panel owns the write path
// (ADR-0003) and the bcrypt hash lives in PasswordHash.
//
// EmailCached is a denormalised `local_part + '@' + domains.name`
// maintained by BEFORE INSERT/UPDATE triggers — we don't write it
// from Go code. Stalwart's queryLogin matches on EmailCached.
type Mailbox struct {
	ID          string `gorm:"type:char(26);primaryKey" json:"id"`
	DomainID    string `gorm:"type:char(26);not null;index:ix_mailboxes_domain" json:"domain_id"`
	LocalPart   string `gorm:"type:varchar(64);not null" json:"local_part"`
	EmailCached string `gorm:"type:varchar(320);not null;uniqueIndex:ux_mailboxes_email_cached" json:"email"`
	// DisplayName is the human-readable name (GH #197). The agent pushes
	// it to the Stalwart account's JMAP description (x:Account/set), which
	// drives the default Identity name shown as the From name in Bulwark
	// webmail. Set over JMAP (not the SQL directory) so it converges on
	// existing installs too.
	DisplayName  string `gorm:"column:display_name;type:varchar(255);not null;default:''" json:"display_name"`
	PasswordHash string `gorm:"type:varchar(255);not null" json:"-"`
	// PasswordEnc is the AES-256-GCM ciphertext of the mailbox's
	// plaintext password, encrypted with /etc/jabali-panel/sso.key.
	// Used exclusively by the webmail SSO flow (M6 Step 8 Phase B) to
	// mint a Bulwark session on behalf of the user. NULL for mailboxes
	// created before migration 000056 landed — next rotate fills it.
	PasswordEnc []byte `gorm:"type:varbinary(512)" json:"-"`
	QuotaBytes  uint64 `gorm:"type:bigint unsigned;not null;default:1073741824" json:"quota_bytes"`
	IsDisabled  bool   `gorm:"type:tinyint(1);not null;default:0" json:"is_disabled"`
	// SendOnly (GH #371) — the account authenticates for SMTP submission
	// but is excluded from delivery (Stalwart directory queryRecipient
	// filters `send_only = 0`), so it can send mail but never receives or
	// stores any. Set at create; toggleable via update.
	SendOnly bool `gorm:"column:send_only;type:tinyint(1);not null;default:0" json:"send_only"`
	// System (GH #1056) — a panel-managed infrastructure principal, not a
	// human mailbox. Set for the JAB-230 sendmail relay (noreply@<domain>,
	// a SendOnly account the PHP mail() shim submits through). Such rows are
	// filtered out of the user-facing mailbox lists so operators don't see a
	// mailbox they never created; every internal path (backup, disk usage,
	// usage ticker) still reads them via the repo unfiltered.
	System         bool       `gorm:"column:system;type:tinyint(1);not null;default:0" json:"-"`
	LastUsageBytes uint64     `gorm:"type:bigint unsigned;not null;default:0" json:"last_usage_bytes"`
	LastUsageAt    *time.Time `gorm:"type:datetime(6)" json:"last_usage_at,omitempty"`
	CreatedAt      time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (Mailbox) TableName() string { return "mailboxes" }

// MailboxSSOToken is a short-lived single-use credential the panel
// issues when a user clicks "Webmail" on a mailbox row. The token's
// plaintext is handed to the user's browser; only its SHA-256 lives
// in the DB so a leaked row can't be used to claim a session.
//
// Consumed by the /sso/webmail landing endpoint which reads the row
// under FOR UPDATE, deletes it, decrypts the linked mailbox's
// password_enc, and POSTs Bulwark's /api/auth/session on behalf of
// the user — Bulwark sets its own cookie and we 302 the browser to /.
type MailboxSSOToken struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	MailboxID string    `gorm:"type:char(26);not null;index:idx_mailbox_sso_mailbox_id" json:"mailbox_id"`
	UserID    string    `gorm:"type:char(26);not null" json:"user_id"`
	TokenHash string    `gorm:"type:char(64);not null;uniqueIndex:uniq_mailbox_sso_token_hash" json:"-"`
	ExpiresAt time.Time `gorm:"type:datetime(6);not null;index:idx_mailbox_sso_expires_at" json:"expires_at"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null;default:CURRENT_TIMESTAMP(6)" json:"created_at"`
}

func (MailboxSSOToken) TableName() string { return "mailbox_sso_tokens" }
