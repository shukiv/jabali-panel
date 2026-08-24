package models

import "time"

// EmailForwarder represents a mail forwarder (either an alias or external forward).
// Stalwart integration: x:UserAccount.aliases + x:SieveUserScript.
// Jabali is truth; reconciler converges to Stalwart on every tick.
//
// Type 'alias': Mail to local_part@domain is delivered to mailbox_id.
// Type 'external': Mail to local_part@domain is forwarded to target (an outside email).
type EmailForwarder struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	MailboxID *string   `gorm:"type:char(26);index:ix_email_forwarders_mailbox" json:"mailbox_id,omitempty"`
	DomainID  string    `gorm:"type:char(26);not null;index:ix_email_forwarders_domain" json:"domain_id"`
	Type      string    `gorm:"type:enum('alias','external');not null" json:"type"`
	LocalPart *string   `gorm:"type:varchar(64)" json:"local_part"` // NULL for type='external'
	Target    string    `gorm:"type:varchar(255);not null" json:"target"`
	Enabled   bool      `gorm:"type:tinyint(1);not null;default:1" json:"enabled"`
	// KeepCopy (GH #237, migration 000177) applies to type='external':
	// when true the agent emits Sieve `redirect :copy` so a copy is kept
	// in the mailbox; when false it's a plain forward. No gorm `default`
	// tag — the API always sets it (alias rows store false).
	KeepCopy  bool      `gorm:"column:keep_copy;type:tinyint(1);not null" json:"keep_copy"`
	ManagedBy string    `gorm:"type:varchar(16);default:'m6.5'" json:"managed_by"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (EmailForwarder) TableName() string { return "email_forwarders" }

// AliasForwarderTarget is the canonical value stored in
// email_forwarders.target for an ALIAS forwarder: the alias's OWN address
// (aliasLocalPart@domain).
//
// The target column is unused at apply time (alias delivery is by
// local_part -> mailbox), but it is part of the
// uq_external_forward(mailbox_id, type, target) unique key. Every alias used
// to default to the SAME mailbox address, so a mailbox's SECOND alias collided
// on that key and failed (GH #280). Using the alias's own address keeps the
// value unique per alias, so one mailbox can hold many aliases.
//
// The HTTP handler and the CLI both call this so they can never drift on the
// stored target again (JAB-319: the CLI had regressed to the mailbox address).
func AliasForwarderTarget(aliasLocalPart, domain string) string {
	return aliasLocalPart + "@" + domain
}
