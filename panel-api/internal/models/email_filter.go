package models

import "time"

// EmailFilter is one Sieve-like inbox-rule entry attached to a
// mailbox. M6.5 stores the rule as raw Sieve text (operator-edited
// or M35-imported); cpanel imports also keep the raw cpanel rule
// body in CpanelRaw for hand-conversion until the panel ships a
// rule-authoring UI.
type EmailFilter struct {
	ID         string    `gorm:"type:char(26);primaryKey" json:"id"`
	MailboxID  string    `gorm:"type:char(26);not null;index:ix_email_filters_mailbox" json:"mailbox_id"`
	Name       string    `gorm:"type:varchar(64);not null" json:"name"`
	SieveText  *string   `gorm:"type:mediumtext" json:"sieve_text,omitempty"`
	CpanelRaw  *string   `gorm:"type:mediumtext" json:"cpanel_raw,omitempty"`
	Priority   int       `gorm:"type:int;not null;default:0" json:"priority"`
	// Enabled has no gorm `default` tag (the column's own DEFAULT 1 stays):
	// GORM writes a defaulted field's default in place of a false, so an
	// import that creates it disabled (inert unless --preserve-source-state)
	// got an active row. Every caller sets it. Same as CronJob.Enabled.
	Enabled    bool      `gorm:"type:tinyint(1);not null" json:"enabled"`
	ManagedBy  string    `gorm:"type:varchar(16);not null;default:'m6.5'" json:"managed_by"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (EmailFilter) TableName() string { return "email_filters" }
