package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// SSLCertificateWithDomain is a read-only projection joining ssl_certificates, domains, and users.
// Used by list endpoints to provide a unified view without follow-up queries.
type SSLCertificateWithDomain struct {
	ID            string     `json:"id"`
	DomainID      string     `json:"domain_id"`
	DomainName    string     `json:"domain_name"`
	UserID        string     `json:"user_id"`
	UserUsername  string     `json:"user_username"`
	Status        string     `json:"status"`
	IssuedAt      *time.Time `json:"issued_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	RenewalCount  int        `json:"renewal_count"`
	LastRenewedAt *time.Time `json:"last_renewed_at"`
	LastError     *string    `json:"last_error"`
	Staging       bool       `json:"staging"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	// CertPath is the certificate file on disk. Carried on the join so the
	// JAB-203 observation pass can read the real notAfter instead of trusting
	// expires_at — and so it reads the ACTUAL path rather than assuming
	// /etc/letsencrypt/live/<domain>/, which is wrong for installed custom
	// certificates.
	CertPath *string `json:"cert_path,omitempty"`
	// Domain flags carried on the ListAll join for the SAN-drift pass
	// (JAB-226): they feed sanHostnamesForDomain so it can tell which SANs
	// an issued cert SHOULD carry, without a per-cert domain lookup. Only
	// ListAll selects them; other list queries leave them zero. Internal —
	// not part of the admin SSL JSON.
	SSLMode       string `json:"-"`
	EmailEnabled  bool   `json:"-"`
	SkipAutoSAN   bool   `json:"-"`
	CreateWWW     bool   `json:"-"`
	MTASTSEnabled bool   `json:"-"`
}

// SSLCertificateRepository covers the ssl_certificates table.
// Tracks ACME certificate lifecycle per domain.
type SSLCertificateRepository interface {
	Create(ctx context.Context, cert *models.SSLCertificate) error
	FindByDomainID(ctx context.Context, domainID string) (*models.SSLCertificate, error)
	FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.SSLCertificate, error)
	UpdateStatus(ctx context.Context, id string, status string, lastError *string) error
	UpdateAfterIssuance(ctx context.Context, id string, issuedAt, expiresAt time.Time, certPath, keyPath string) error
	SetIssueMethod(ctx context.Context, id, method string) error
	UpdateAfterRenewal(ctx context.Context, id string, issuedAt, expiresAt time.Time, certPath, keyPath string) error
	MarkRevoked(ctx context.Context, id string) error
	DeleteByDomainID(ctx context.Context, domainID string) error
	// RefreshObservedExpiry corrects expires_at from the certificate actually on
	// disk (JAB-203). certbot's timer renews outside the panel, so nothing
	// refreshed this column and it drifted — measured 2.5 months stale on a live
	// box, while the expiry alerts compute off it.
	//
	// Targeted UPDATE of the observed columns only: this pass must never touch
	// status, retry bookkeeping or paths, which belong to the issuance flow.
	// last_renewed_at moves only when the certificate moved FORWARD, so it keeps
	// meaning "when this cert last got newer" rather than "when we last looked".
	RefreshObservedExpiry(ctx context.Context, id string, notAfter, observedAt time.Time) error
	ListAll(ctx context.Context) ([]SSLCertificateWithDomain, error)
	ListByUserID(ctx context.Context, userID string) ([]SSLCertificateWithDomain, error)
	UpdateSelfSigned(ctx context.Context, id string, certPath, keyPath string, expiresAt time.Time) error
	UpdateCustom(ctx context.Context, id string, certPath, keyPath string, expiresAt time.Time) error
	UpdateAfterACMEFailure(ctx context.Context, id string, lastError string, nextRetryAt time.Time, retryCount int, fallbackCertPath, fallbackKeyPath *string, fallbackExpiresAt *time.Time) error
	UpdateAfterACMEFailureCapped(ctx context.Context, id string, lastError string, retryCount int, fallbackCertPath, fallbackKeyPath *string, fallbackExpiresAt *time.Time) error
	MarkFailed(ctx context.Context, id string, lastError string) error
	ListDueForACMERetry(ctx context.Context, now time.Time, limit int) ([]models.SSLCertificate, error)
	// ListExhaustedForSSLEnabledDomains returns certificates that gave up
	// (status='failed') on domains where SSL is still wanted. JAB-224.
	ListExhaustedForSSLEnabledDomains(ctx context.Context, before time.Time, limit int) ([]models.SSLCertificate, error)
	// RearmACME puts an exhausted certificate back in the retry queue.
	RearmACME(ctx context.Context, id string, retryCount int, now time.Time) error
}

type sslCertificateRepo struct{ db *gorm.DB }

// NewSSLCertificateRepository returns an SSLCertificateRepository bound to a GORM DB.
func NewSSLCertificateRepository(db *gorm.DB) SSLCertificateRepository {
	return &sslCertificateRepo{db: db}
}

// Create inserts a new SSL certificate record.
func (r *sslCertificateRepo) Create(ctx context.Context, cert *models.SSLCertificate) error {
	return r.db.WithContext(ctx).Create(cert).Error
}

// FindByDomainID retrieves the SSL certificate for a domain.
// Returns ErrNotFound if no certificate exists; other errors are DB errors.
func (r *sslCertificateRepo) FindByDomainID(ctx context.Context, domainID string) (*models.SSLCertificate, error) {
	var cert models.SSLCertificate
	err := r.db.WithContext(ctx).First(&cert, "domain_id = ?", domainID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// UpdateStatus updates the certificate's status and optional last error.
// Useful for transitions like pending → issuing or issuing → issued/failed.
func (r *sslCertificateRepo) UpdateStatus(ctx context.Context, id string, status string, lastError *string) error {
	updates := map[string]interface{}{
		"status":     status,
		"last_error": lastError,
		"updated_at": time.Now(),
	}
	if status == models.SSLStatusFailed {
		updates["last_attempt_at"] = time.Now()
	}
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(updates).Error
}

// UpdateAfterIssuance updates issuance metadata: issued_at, expires_at, cert_path, key_path.
// Called after a successful ACME cert issue or renewal.
func (r *sslCertificateRepo) UpdateAfterIssuance(ctx context.Context, id string, issuedAt, expiresAt time.Time, certPath, keyPath string) error {
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"issued_at":       issuedAt,
			"expires_at":      expiresAt,
			"cert_path":       certPath,
			"key_path":        keyPath,
			"status":          models.SSLStatusIssued,
			"last_attempt_at": time.Now(),
			"updated_at":      time.Now(),
		}).Error
}

// SetIssueMethod records which ACME challenge type produced the current
// cert ('http-01' | 'dns-01', JAB-235). Separate from UpdateAfterIssuance
// so its many call sites keep their signature.
func (r *sslCertificateRepo) SetIssueMethod(ctx context.Context, id, method string) error {
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Update("issue_method", method).Error
}

// UpdateAfterRenewal does what UpdateAfterIssuance does plus bumps the
// renewal_count and stamps last_renewed_at. Used by the reconciler after a
// successful ssl.renew agent call.
func (r *sslCertificateRepo) UpdateAfterRenewal(ctx context.Context, id string, issuedAt, expiresAt time.Time, certPath, keyPath string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"issued_at":       issuedAt,
			"expires_at":      expiresAt,
			"cert_path":       certPath,
			"key_path":        keyPath,
			"status":          models.SSLStatusIssued,
			"last_renewed_at": now,
			"renewal_count":   gorm.Expr("renewal_count + 1"),
			"last_error":      nil,
			"last_attempt_at": now,
			"updated_at":      now,
		}).Error
}

// MarkRevoked flips status='revoked' and clears cert/key paths + last_error.
// Called after a successful ssl.revoke; the nginx vhost regen that runs next
// will read the cleared paths and drop the 443 server block.
func (r *sslCertificateRepo) MarkRevoked(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":     models.SSLStatusRevoked,
			"cert_path":  nil,
			"key_path":   nil,
			"last_error": nil,
			"updated_at": time.Now(),
		}).Error
}

// DeleteByDomainID removes the SSL certificate for a domain.
// Called when SSL is disabled for a domain or domain is deleted.
func (r *sslCertificateRepo) DeleteByDomainID(ctx context.Context, domainID string) error {
	return r.db.WithContext(ctx).Delete(&models.SSLCertificate{}, "domain_id = ?", domainID).Error
}

// RefreshObservedExpiry — see the interface.
func (r *sslCertificateRepo) RefreshObservedExpiry(ctx context.Context, id string, notAfter, observedAt time.Time) error {
	updates := map[string]any{
		"expires_at": notAfter,
		"updated_at": observedAt,
	}
	// Only stamp last_renewed_at when the certificate genuinely moved forward.
	// A row being corrected BACKWARDS (panel ahead of disk) is a bookkeeping
	// fix, not a renewal, and recording it as one would hide a cert that never
	// actually renewed.
	res := r.db.WithContext(ctx).
		Model(&models.SSLCertificate{}).
		Where("id = ? AND (expires_at IS NULL OR expires_at < ?)", id, notAfter).
		Updates(map[string]any{
			"expires_at":      notAfter,
			"last_renewed_at": observedAt,
			"updated_at":      observedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// Certificate did not move forward — correct expires_at alone.
	return r.db.WithContext(ctx).
		Model(&models.SSLCertificate{}).
		Where("id = ?", id).
		Updates(updates).Error
}

// ListAll returns all SSL certificates joined with their domain and user info.
// Used by admin to view all certificates across all domains and users.
func (r *sslCertificateRepo) ListAll(ctx context.Context) ([]SSLCertificateWithDomain, error) {
	var results []SSLCertificateWithDomain
	err := r.db.WithContext(ctx).
		Select(`sc.id, sc.domain_id, d.name as domain_name,
		        d.user_id, u.username as user_username,
		        sc.status, sc.issued_at, sc.expires_at,
		        sc.renewal_count, sc.last_renewed_at, sc.last_error, sc.staging, sc.last_attempt_at,
		        sc.cert_path,
		        d.ssl_mode, d.email_enabled, d.skip_auto_san, d.create_www, d.mta_sts_enabled`).
		Table("ssl_certificates sc").
		Joins("JOIN domains d ON sc.domain_id = d.id").
		Joins("JOIN users u ON d.user_id = u.id").
		Where("sc.status <> ?", models.SSLStatusRevoked).
		Order("sc.created_at DESC").
		Scan(&results).Error
	if err != nil {
		return nil, err
	}
	return results, nil
}

// ListByUserID returns all SSL certificates for a specific user,
// joining with domain and user info. Used by users to view their own certificates.
func (r *sslCertificateRepo) ListByUserID(ctx context.Context, userID string) ([]SSLCertificateWithDomain, error) {
	var results []SSLCertificateWithDomain
	err := r.db.WithContext(ctx).
		Select(`sc.id, sc.domain_id, d.name as domain_name,
		        d.user_id, u.username as user_username,
		        sc.status, sc.issued_at, sc.expires_at,
		        sc.renewal_count, sc.last_renewed_at, sc.last_error, sc.staging, sc.last_attempt_at`).
		Table("ssl_certificates sc").
		Joins("JOIN domains d ON sc.domain_id = d.id").
		Joins("JOIN users u ON d.user_id = u.id").
		Where("d.user_id = ?", userID).
		Where("sc.status <> ?", models.SSLStatusRevoked).
		Order("sc.created_at DESC").
		Scan(&results).Error
	if err != nil {
		return nil, err
	}
	return results, nil
}

// UpdateSelfSigned sets the certificate to self-signed fallback status
// with the given cert/key paths and expiration, clearing last_error.
func (r *sslCertificateRepo) UpdateSelfSigned(ctx context.Context, id string, certPath, keyPath string, expiresAt time.Time) error {
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":          models.SSLStatusSelfSigned,
			"cert_path":       certPath,
			"key_path":        keyPath,
			"expires_at":      expiresAt,
			"last_error":      nil,
			"last_attempt_at": time.Now(),
			"updated_at":      time.Now(),
		}).Error
}

// UpdateCustom sets the certificate to operator-supplied 'custom' status with
// the installed paths + parsed expiry (GH #246). No auto-renew.
func (r *sslCertificateRepo) UpdateCustom(ctx context.Context, id string, certPath, keyPath string, expiresAt time.Time) error {
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":          models.SSLStatusCustom,
			"cert_path":       certPath,
			"key_path":        keyPath,
			"expires_at":      expiresAt,
			"last_error":      nil,
			"last_attempt_at": time.Now(),
			"updated_at":      time.Now(),
		}).Error
}

// UpdateAfterACMEFailure sets status to pending_acme_retry, records the error,
// and schedules the next retry via next_retry_at and retry_count.
// If fallback paths are provided, also writes those (for first failure with self-signed).
func (r *sslCertificateRepo) UpdateAfterACMEFailure(ctx context.Context, id string, lastError string, nextRetryAt time.Time, retryCount int, fallbackCertPath, fallbackKeyPath *string, fallbackExpiresAt *time.Time) error {
	updates := map[string]interface{}{
		"status":          models.SSLStatusPendingACMERetry,
		"last_error":      lastError,
		"next_retry_at":   nextRetryAt,
		"retry_count":     retryCount,
		"last_attempt_at": time.Now(),
		"updated_at":      time.Now(),
	}
	if fallbackCertPath != nil {
		updates["cert_path"] = *fallbackCertPath
	}
	if fallbackKeyPath != nil {
		updates["key_path"] = *fallbackKeyPath
	}
	if fallbackExpiresAt != nil {
		updates["expires_at"] = *fallbackExpiresAt
	}
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(updates).Error
}

// UpdateAfterACMEFailureCapped sets status='failed' (no further retries),
// records the last error + retry_count, AND writes fallback cert paths
// when the first failure happened to seed a self-signed cert. Used by
// the reconciler when retry_count hits the acmeMaxRetries cap.
//
// Differs from MarkFailed in that it preserves the retry_count + the
// fallback cert paths so the UI can still show the self-signed cert
// the tenant is currently serving.
func (r *sslCertificateRepo) UpdateAfterACMEFailureCapped(ctx context.Context, id string, lastError string, retryCount int, fallbackCertPath, fallbackKeyPath *string, fallbackExpiresAt *time.Time) error {
	updates := map[string]interface{}{
		"status":          models.SSLStatusFailed,
		"last_error":      lastError,
		"next_retry_at":   nil,
		"retry_count":     retryCount,
		"last_attempt_at": time.Now(),
		"updated_at":      time.Now(),
	}
	if fallbackCertPath != nil {
		updates["cert_path"] = *fallbackCertPath
	}
	if fallbackKeyPath != nil {
		updates["key_path"] = *fallbackKeyPath
	}
	if fallbackExpiresAt != nil {
		updates["expires_at"] = *fallbackExpiresAt
	}
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(updates).Error
}

// MarkFailed sets status='failed' and clears next_retry_at (manual retry only).
func (r *sslCertificateRepo) MarkFailed(ctx context.Context, id string, lastError string) error {
	return r.db.WithContext(ctx).Model(&models.SSLCertificate{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":          models.SSLStatusFailed,
			"last_error":      lastError,
			"next_retry_at":   nil,
			"last_attempt_at": time.Now(),
			"updated_at":      time.Now(),
		}).Error
}

// ListDueForACMERetry returns certificates the SSL retry ticker should attempt
// to (re-)issue right now. Two cases qualify:
//
//  1. status='pending' — the row was just created (or operator-reset) and has
//     never had ACME run against it. There is no next_retry_at yet, so the
//     ticker is the first thing to pick it up after the API hands it off.
//
//  2. status='pending_acme_retry' AND next_retry_at <= now — the row has had
//     at least one failed ACME attempt and is now due for the next try.
//
// 'issued' / 'self_signed' / 'failed' / 'renewing' are deliberately excluded:
// 'issued' is steady state (renewals go through the renewal ticker), 'failed'
// is operator-only (manual reset to 'pending'), and 'renewing' is in-flight.
func (r *sslCertificateRepo) ListDueForACMERetry(ctx context.Context, now time.Time, limit int) ([]models.SSLCertificate, error) {
	var certs []models.SSLCertificate
	err := r.db.WithContext(ctx).
		Where("status = ? OR (status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?)",
			models.SSLStatusPending,
			models.SSLStatusPendingACMERetry, now).
		Order("created_at ASC").
		Limit(limit).
		Find(&certs).Error
	if err != nil {
		return nil, err
	}
	return certs, nil
}

// FindByDomainIDs fetches SSL certificates for multiple domains
func (r *sslCertificateRepo) FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.SSLCertificate, error) {
	var certs []models.SSLCertificate
	err := r.db.WithContext(ctx).Where("domain_id IN ?", domainIDs).Find(&certs).Error
	if err != nil {
		return nil, err
	}
	return certs, nil
}

// ListExhaustedForSSLEnabledDomains returns certificates that exhausted their
// ACME attempts (status='failed') while the domain still has SSL enabled, and
// whose last attempt is older than `before`.
//
// JAB-224. These are the certificates ListDueForACMERetry deliberately ignores.
// That exclusion is right for a cert that failed because something is broken,
// and wrong for one that failed because the domain had not cut over yet: an
// un-cut-over domain fails HTTP-01 every single time, by definition, so it
// always exhausts its attempts and then stops retrying — including at the
// moment cutover finally makes issuance possible.
func (r *sslCertificateRepo) ListExhaustedForSSLEnabledDomains(ctx context.Context, before time.Time, limit int) ([]models.SSLCertificate, error) {
	var certs []models.SSLCertificate
	err := r.db.WithContext(ctx).
		Joins("JOIN domains ON domains.id = ssl_certificates.domain_id").
		Where("ssl_certificates.status = ?", models.SSLStatusFailed).
		Where("domains.ssl_enabled = ?", true).
		Where("ssl_certificates.last_attempt_at IS NULL OR ssl_certificates.last_attempt_at <= ?", before).
		Order("ssl_certificates.last_attempt_at ASC").
		Limit(limit).
		Find(&certs).Error
	return certs, err
}

// RearmACME moves an exhausted certificate back to pending_acme_retry, due now.
//
// retryCount is set rather than zeroed so the caller controls how many attempts
// this round buys — re-arming to 0 would hand out a full fresh budget every
// cycle and could push past Let's Encrypt's failed-validation limits.
func (r *sslCertificateRepo) RearmACME(ctx context.Context, id string, retryCount int, now time.Time) error {
	return r.db.WithContext(ctx).
		Model(&models.SSLCertificate{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":        models.SSLStatusPendingACMERetry,
			"retry_count":   retryCount,
			"next_retry_at": now,
			"updated_at":    now,
		}).Error
}
