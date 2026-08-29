package models

import "time"

// Database is a hosted database provisioned by the panel for a user.
type Database struct {
	ID        string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID    string `gorm:"type:char(26);not null" json:"user_id"`
	Name      string `gorm:"type:varchar(64);not null" json:"name"`
	Engine    string `gorm:"type:enum('mariadb','postgres');not null;default:'mariadb'" json:"engine"`
	Charset   string `gorm:"type:varchar(32);not null;default:'utf8mb4'" json:"charset"`
	Collation string `gorm:"type:varchar(32);not null;default:'utf8mb4_unicode_ci'" json:"collation"`
	// SizeBytes is the database's on-disk size (data+index), refreshed by the
	// DB-usage sweeper (GH #1242) so the User List can sum total storage. 0 until
	// first swept.
	SizeBytes     uint64     `gorm:"type:bigint unsigned;not null;default:0" json:"size_bytes"`
	SizeCheckedAt *time.Time `gorm:"type:datetime(6)" json:"size_checked_at,omitempty"`
	CreatedAt     time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (Database) TableName() string { return "databases" }
