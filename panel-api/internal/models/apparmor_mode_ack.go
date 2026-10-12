package models

import "time"

// AppArmorModeAck records that an administrator acknowledged an AppArmor
// profile kept in a non-enforcing mode on purpose (GH #2078). The Server Status
// "not enforcing" alert leaves the profile out while it is still in Mode; the
// panel deletes the row once the profile is in any other mode.
type AppArmorModeAck struct {
	Profile        string    `gorm:"column:profile;type:varchar(64);primaryKey" json:"profile"`
	Mode           string    `gorm:"column:mode;type:varchar(16);not null" json:"mode"`
	AcknowledgedBy *string   `gorm:"column:acknowledged_by;type:char(26)" json:"acknowledged_by,omitempty"`
	AcknowledgedAt time.Time `gorm:"column:acknowledged_at;type:datetime(6);not null" json:"acknowledged_at"`
}

func (AppArmorModeAck) TableName() string { return "apparmor_mode_acks" }
