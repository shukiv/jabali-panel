package models

import "time"

// SharedCertificate is a wildcard / multi-SAN TLS certificate uploaded once and
// served from MANY domains (JAB-170), instead of re-uploading the same cert per
// subdomain. A domain attaches via domains.shared_certificate_id + ssl_mode
// 'shared'. Replacing the cert re-renders every attached vhost in one action —
// the payoff over per-domain custom certs.
//
// The cert/key live as ONE pair on disk (not copied per domain); the private
// key is never readable via the API. UserID NULL = server-wide (admin-owned,
// attachable to any domain); otherwise the owning tenant (attachable only to
// domains they own).
type SharedCertificate struct {
	ID       string  `gorm:"type:char(26);primaryKey"                       json:"id"`
	Name     string  `gorm:"type:varchar(255);not null"                     json:"name"`
	UserID   *string `gorm:"column:user_id;type:char(26);index:ix_shared_cert_user" json:"user_id,omitempty"`
	CertPath *string `gorm:"type:varchar(512)"                              json:"cert_path,omitempty"`
	KeyPath  *string `gorm:"type:varchar(512)"                              json:"key_path,omitempty"`
	// SANs is a JSON array of the certificate's Subject Alternative Names,
	// parsed at upload and used for wildcard-aware attach matching.
	SANs      *string    `gorm:"column:sans;type:text"                          json:"sans,omitempty"`
	ExpiresAt *time.Time `gorm:"type:datetime(6);index:ix_shared_cert_expires"  json:"expires_at,omitempty"`
	CreatedAt time.Time  `gorm:"type:datetime(6);not null"                      json:"created_at"`
	UpdatedAt time.Time  `gorm:"type:datetime(6);not null"                      json:"updated_at"`
}

func (SharedCertificate) TableName() string { return "shared_certificates" }
