package models

import (
	"errors"
	"net"
	"strings"
	"time"
)

// Panel certificate status state machine. See ADR-0066.
//
// self_signed → starting state and the fallback. The cert at
//   /etc/jabali/tls/panel.crt is the openssl-generated SAN cert
//   install.sh produced via provision_tls_cert.
// pending_acme → admin flipped use_le on (or M32 reconciler decided
//   to attempt because routable + use_le=1). Reconciler will dispatch
//   ssl.panel.issue on the next tick.
// issued → certbot returned a fresh lineage; deploy-hook copied
//   fullchain/privkey into /etc/jabali/tls/panel.{crt,key} and
//   reloaded nginx + jabali-panel + jabali-bulwark. expires_at
//   carries the LE notAfter.
// pending_acme_retry → certbot attempt failed once; last_error
//   carries the reason. Reconciler retries every 3h until either
//   issued or admin flips use_le off.
// failed → terminal flag for non-retryable errors (rate-limit
//   exhausted, hostname permanently unroutable). M32.1 will surface
//   how to clear this back to pending_acme.
const (
	PanelCertStatusSelfSigned       = "self_signed"
	PanelCertStatusPendingACME      = "pending_acme"
	PanelCertStatusIssued           = "issued"
	PanelCertStatusPendingACMERetry = "pending_acme_retry"
	PanelCertStatusFailed           = "failed"
)

// Panel cert kinds. Two independent rows (ADR-0105): the panel
// hostname cert and the panel mail (mail.<hostname>) cert. Each owns
// the full status state machine + its own cert path / retry.
const (
	PanelCertKindHostname = "hostname"
	PanelCertKindMail     = "mail"
)

// PanelMailHostname is the mail SAN derived from the panel hostname.
// It is the derivation primitive; consumers that must honour an operator
// override read EffectiveMailHostname, not this, directly.
func PanelMailHostname(hostname string) string {
	if hostname == "" {
		return ""
	}
	return "mail." + hostname
}

// Mail-hostname validation sentinels (JAB-390). Each names one rejected
// shape so a caller — and the falsification tests — can pin the exact arm.
var (
	ErrMailHostnameEmpty      = errors.New("mail hostname is empty")
	ErrMailHostnameWhitespace = errors.New("mail hostname contains whitespace")
	ErrMailHostnameNotBare    = errors.New("mail hostname must be a bare host (no scheme, path, or userinfo)")
	ErrMailHostnameWildcard   = errors.New("mail hostname must not be a wildcard")
	ErrMailHostnamePort       = errors.New("mail hostname must not carry a port")
	ErrMailHostnameDot        = errors.New("mail hostname must not begin or end with a dot")
	ErrMailHostnameNotFQDN    = errors.New("mail hostname must be a fully-qualified domain name")
	ErrMailHostnameIP         = errors.New("mail hostname must be a name, not an IP address")
	ErrMailHostnameTooLong    = errors.New("mail hostname exceeds 253 characters")
	ErrMailHostnameLabel      = errors.New("mail hostname has an invalid label")
)

// ValidateMailHostname normalizes and validates a bare mail-hostname
// FQDN (JAB-390). On success it returns the trimmed, lowercased host.
// It rejects an empty value, internal whitespace, a scheme / path /
// userinfo, a wildcard, a port, a leading or trailing dot, a single
// (non-FQDN) label, an IP literal, an over-length host, and any label
// that is empty, over 63 bytes, hyphen-bounded, or has a character
// outside [a-z0-9-]. This is the one gate the future override setter
// validates against, and the fail-safe read guard EffectiveMailHostname
// applies to a stored value.
func ValidateMailHostname(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	switch {
	case h == "":
		return "", ErrMailHostnameEmpty
	case strings.ContainsAny(h, " \t\r\n\v\f"):
		// TrimSpace stripped the ends, so any survivor is internal.
		return "", ErrMailHostnameWhitespace
	case strings.Contains(h, "://") || strings.ContainsAny(h, "/\\?#@"):
		return "", ErrMailHostnameNotBare
	case strings.Contains(h, "*"):
		return "", ErrMailHostnameWildcard
	case strings.Contains(h, ":"):
		return "", ErrMailHostnamePort
	case strings.HasPrefix(h, ".") || strings.HasSuffix(h, "."):
		return "", ErrMailHostnameDot
	case !strings.Contains(h, "."):
		return "", ErrMailHostnameNotFQDN
	case net.ParseIP(h) != nil:
		return "", ErrMailHostnameIP
	case len(h) > 253:
		return "", ErrMailHostnameTooLong
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 ||
			strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", ErrMailHostnameLabel
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return "", ErrMailHostnameLabel
			}
		}
	}
	return h, nil
}

// EffectiveMailHostname resolves the panel mail hostname that every
// consumer of the panel mail identity must use (JAB-390): the APPLIED
// mail hostname (server_settings.mail_hostname) when it is set AND passes
// ValidateMailHostname, otherwise the derived mail.<panelHostname>
// (PanelMailHostname). Routing all consumers through this one function is
// what lets a switchover propagate from a single place.
//
// A stored value that fails validation is ignored in favour of the
// derived name — a fail-safe read guard so a corrupt or hand-edited row
// can never push a bogus host into a certificate SAN or a relay
// credential. On success it returns the normalized (trimmed, lowercased)
// value.
func EffectiveMailHostname(override *string, panelHostname string) string {
	if applied, ok := AppliedMailHostname(override); ok {
		return applied
	}
	return PanelMailHostname(panelHostname)
}

// AppliedMailHostname reports the custom mail hostname in effect: the
// normalized stored value and true when it passes ValidateMailHostname.
// NULL, empty or invalid returns ("", false), meaning the derived
// mail.<panel-hostname> is in effect. Use it where a caller must tell a
// custom hostname apart from the derived default (the settings view);
// consumers that only need the name use EffectiveMailHostname.
func AppliedMailHostname(stored *string) (string, bool) {
	if stored == nil {
		return "", false
	}
	norm, err := ValidateMailHostname(*stored)
	if err != nil {
		return "", false
	}
	return norm, true
}

// PanelCertificate is the singleton (id=1) row tracking the panel
// hostname's TLS cert lifecycle. Empty fields on first boot are
// seeded by the application; the migration only creates the table.
type PanelCertificate struct {
	Kind          string     `gorm:"primaryKey;type:varchar(16);not null;default:'hostname'" json:"kind"`
	ID            uint8      `gorm:"type:tinyint unsigned;not null;default:1"          json:"id"`
	Hostname      string     `gorm:"type:varchar(253);not null;default:''"             json:"hostname"`
	Status        string     `gorm:"type:varchar(32);not null;default:'self_signed'"   json:"status"`
	CertPEMPath   string     `gorm:"type:varchar(255);not null;default:'/etc/jabali/tls/panel.crt'" json:"cert_pem_path"`
	IssuedAt      *time.Time `                                                         json:"issued_at,omitempty"`
	ExpiresAt     *time.Time `                                                         json:"expires_at,omitempty"`
	LastError     string     `gorm:"type:text"                                         json:"last_error,omitempty"`
	AttemptCount  uint32     `gorm:"type:int unsigned;not null;default:0"              json:"attempt_count"`
	NextRetryAt   *time.Time `                                                         json:"next_retry_at,omitempty"`
	Staging       bool       `gorm:"type:tinyint(1);not null;default:0"                json:"staging"`
	UseLE         bool       `gorm:"type:tinyint(1);not null;default:0"                json:"use_le"`
	UpdatedAt     time.Time  `gorm:"autoUpdateTime"                                    json:"updated_at"`
}

// TableName pins the GORM table name to the migration's spelling.
// Without this GORM would pluralise PanelCertificate to
// "panel_certificates", which doesn't exist.
func (PanelCertificate) TableName() string { return "panel_certificate" }
