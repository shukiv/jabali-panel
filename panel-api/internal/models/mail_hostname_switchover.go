package models

import "time"

// Mail hostname switchover states (JAB-390, migration 000305).
const (
	// MailHostnameSwitchoverIdle: no request.
	MailHostnameSwitchoverIdle = "idle"
	// MailHostnameSwitchoverPending: requested, not attempted yet.
	MailHostnameSwitchoverPending = "pending"
	// MailHostnameSwitchoverIssuing: an attempt is in flight.
	MailHostnameSwitchoverIssuing = "issuing"
	// MailHostnameSwitchoverFailed: the last attempt failed; retried at
	// NextRetryAt.
	MailHostnameSwitchoverFailed = "failed"
	// MailHostnameSwitchoverDone: the desired name is applied.
	MailHostnameSwitchoverDone = "done"
)

// MailHostnameSwitchover is the singleton (id=1) request to move the panel
// mail hostname (JAB-390). Desired is what an admin asked for;
// server_settings.mail_hostname is what is applied. The reconciler moves the
// applied name only after the desired one is routable and carries a
// certificate.
type MailHostnameSwitchover struct {
	ID          uint8      `gorm:"primaryKey;type:tinyint unsigned;not null;default:1" json:"-"`
	Desired     *string    `gorm:"type:varchar(253)"                                   json:"desired"`
	Status      string     `gorm:"type:varchar(16);not null;default:'idle'"            json:"status"`
	LastError   string     `gorm:"type:varchar(1024);not null;default:''"              json:"last_error"`
	Attempts    uint32     `gorm:"type:int unsigned;not null;default:0"                json:"attempts"`
	NextRetryAt *time.Time `                                                           json:"next_retry_at"`
	RequestedBy string     `gorm:"type:varchar(64);not null;default:''"                json:"requested_by"`
	RequestedAt *time.Time `                                                           json:"requested_at"`
	UpdatedAt   time.Time  `gorm:"type:datetime(3);not null"                           json:"updated_at"`
}

// TableName pins the migration's spelling (GORM would pluralise it).
func (MailHostnameSwitchover) TableName() string { return "mail_hostname_switchover" }
