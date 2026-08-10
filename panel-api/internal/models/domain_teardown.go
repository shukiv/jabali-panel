package models

import "time"

// DomainTeardown is the durable handle for a deleted domain's host-side
// teardown (JAB-236). The panel row is the only natural handle and it is
// gone by design once the operator deletes the domain — this tombstone is
// written BEFORE the row delete and removed only after the agent-side
// teardown (Stalwart purge, nginx vhost, pdns zone) verifiably succeeds,
// so a panel restart or agent outage mid-delete can no longer leave a
// deleted domain serving.
type DomainTeardown struct {
	DomainName    string     `gorm:"column:domain_name;type:varchar(255);primaryKey" json:"domain_name"`
	Attempts      int        `gorm:"column:attempts;type:int;not null;default:0" json:"attempts"`
	LastError     string     `gorm:"column:last_error;type:varchar(1024);not null;default:''" json:"last_error"`
	LastAttemptAt *time.Time `gorm:"column:last_attempt_at;type:datetime(6)" json:"last_attempt_at,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
}

func (DomainTeardown) TableName() string { return "domain_teardowns" }
