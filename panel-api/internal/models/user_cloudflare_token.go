package models

import "time"

// UserCloudflareToken is a tenant's own Cloudflare API token for ACME DNS-01
// (GH #2032). TokenEnc is sealed with the sso key and never leaves the panel:
// no API response, log line or backup carries it.
type UserCloudflareToken struct {
	UserID     string    `gorm:"column:user_id;type:char(26);primaryKey" json:"user_id"`
	TokenEnc   []byte    `gorm:"column:token_enc;type:varbinary(512);not null" json:"-"`
	VerifiedAt time.Time `gorm:"column:verified_at;type:datetime(6);not null" json:"verified_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (UserCloudflareToken) TableName() string { return "user_cloudflare_tokens" }
