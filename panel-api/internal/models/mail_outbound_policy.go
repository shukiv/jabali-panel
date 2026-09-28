package models

import "time"

// MailOutboundPolicy is one outbound rate-cap row in the
// mail_outbound_policy table (mig 000139, 000144, 000145, 000146).
//
// scope ∈ {user, domain, global}. scope_ref is the sender address
// (scope=user) or sender domain (scope=domain), NULL for scope=global —
// a single server-wide cap. Mig 000145 widened it from a ULID column.
//
// max_per_hour / max_per_day = 0 means unlimited. The reconciler
// (Wave 3) converges each enabled row into up to two Stalwart
// MtaOutboundThrottle objects through the agent's mail.throttle.* verbs;
// their ids live in stalwart_id (hourly) and stalwart_id_daily so later
// updates and deletes target the right objects.
type MailOutboundPolicy struct {
	ID            string     `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	Scope         string     `gorm:"column:scope;type:varchar(16);not null" json:"scope"`
	ScopeRef      *string    `gorm:"column:scope_ref;type:varchar(320)" json:"scope_ref,omitempty"`
	MaxPerHour    uint       `gorm:"column:max_per_hour;type:int unsigned;not null;default:0" json:"max_per_hour"`
	MaxPerDay     uint       `gorm:"column:max_per_day;type:int unsigned;not null;default:0" json:"max_per_day"`
	Enabled       bool       `gorm:"column:enabled;type:tinyint(1);not null;default:1" json:"enabled"`
	StalwartID    string     `gorm:"column:stalwart_id;type:varchar(64);not null;default:''" json:"stalwart_id"`
	// StalwartIDDaily tracks the SECOND Stalwart throttle when both
	// hourly + daily caps are set (mig 000146 / ADR-0112 v3). Empty
	// when only one rate window is active.
	StalwartIDDaily string `gorm:"column:stalwart_id_daily;type:varchar(64);not null;default:''" json:"stalwart_id_daily"`
	LastAppliedAt *time.Time `gorm:"column:last_applied_at;type:datetime(6)" json:"last_applied_at,omitempty"`
	LastError     *string    `gorm:"column:last_error;type:text" json:"last_error,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6)" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6)" json:"updated_at"`
}

func (MailOutboundPolicy) TableName() string { return "mail_outbound_policy" }

// Scope constants — keep aligned with the migration's VARCHAR check
// (none enforced at DB level; the repo + handler reject anything else).
const (
	OutboundScopeUser   = "user"
	OutboundScopeDomain = "domain"
	OutboundScopeGlobal = "global"
)
