package models

import (
	"strings"
	"time"
)

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
	SANs *string `gorm:"column:sans;type:text"                          json:"sans,omitempty"`
	// Source distinguishes operator-uploaded material ('uploaded') from
	// panel-issued ACME DNS-01 certs ('acme'). ACME rows renew via
	// certbot; uploaded rows only warn on expiry.
	Source string `gorm:"type:varchar(16);not null;default:'uploaded'"   json:"source"`
	// LastError holds the most recent failed ACME attempt, cleared on
	// success. Always nil for uploaded certs.
	LastError *string `gorm:"type:text"                                      json:"last_error,omitempty"`
	// Staging marks a Let's Encrypt staging-CA cert (untrusted by
	// browsers — issued that way for testing only).
	Staging bool `gorm:"type:tinyint(1);not null;default:0"             json:"staging"`
	// LastAttemptAt is the most recent ACME issue/renew attempt (success
	// or failure) — the reconciler's backoff anchor. Nil until first try.
	LastAttemptAt *time.Time `gorm:"type:datetime(6)"                              json:"last_attempt_at,omitempty"`
	ExpiresAt *time.Time `gorm:"type:datetime(6);index:ix_shared_cert_expires"  json:"expires_at,omitempty"`
	CreatedAt time.Time  `gorm:"type:datetime(6);not null"                      json:"created_at"`
	UpdatedAt time.Time  `gorm:"type:datetime(6);not null"                      json:"updated_at"`
}

// ACMELineageName is the certbot --cert-name for this row: certbot
// forbids '*' in lineage names, so "*.example.com" pins as
// "wildcard.example.com". Non-wildcard names pass through.
func (c *SharedCertificate) ACMELineageName() string {
	if strings.HasPrefix(c.Name, "*.") {
		return "wildcard." + strings.TrimPrefix(c.Name, "*.")
	}
	return c.Name
}

// SharedCertificate.Source values.
const (
	SharedCertSourceUploaded = "uploaded"
	SharedCertSourceACME     = "acme"
)

func (SharedCertificate) TableName() string { return "shared_certificates" }
