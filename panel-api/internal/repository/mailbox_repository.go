package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MailboxRepository defines data access for per-domain mailboxes
// (ADR-0042). The SqlDirectory under Stalwart reads this table on
// every auth; panel-API is the only writer.
//
// EmailCached is maintained by BEFORE INSERT/UPDATE triggers from
// migration 000054 — we never set it here directly.
type MailboxRepository interface {
	FindByID(ctx context.Context, id string) (*models.Mailbox, error)
	// FindByIDs batch-loads mailboxes by id (deduped, one query) — the
	// N+1-free path for list handlers that resolve many rows (JAB-147).
	FindByIDs(ctx context.Context, ids []string) ([]models.Mailbox, error)
	FindByEmail(ctx context.Context, email string) (*models.Mailbox, error)
	ListByDomainID(ctx context.Context, domainID string, opts ListOptions) ([]models.Mailbox, int64, error)
	ListAllWithDomain(ctx context.Context) ([]MailboxWithDomain, error)
	// CountAll counts mailboxes without loading rows (see implementation).
	CountAll(ctx context.Context) (int64, error)
	ListByOwnerWithDomain(ctx context.Context, userID string) ([]MailboxWithDomain, error)
	CountByDomainID(ctx context.Context, domainID string) (int64, error)
	Create(ctx context.Context, mb *models.Mailbox) error
	Delete(ctx context.Context, id string) error
	UpdatePasswordHash(ctx context.Context, id string, hash string) error
	// UpdatePasswordHashAndEnc writes both the bcrypt hash (used by
	// Stalwart's SqlDirectory for IMAP/SMTP/JMAP auth) and the
	// AES-256-GCM envelope of the plaintext (used by the webmail SSO
	// landing to drive Bulwark's /api/auth/session). Both must be
	// updated atomically so an SSO-mint can't hand out a token that
	// decrypts to a password Stalwart no longer accepts.
	UpdatePasswordHashAndEnc(ctx context.Context, id string, hash string, enc []byte) error
	UpdateQuota(ctx context.Context, id string, quotaBytes uint64) error
	UpdateDisplayName(ctx context.Context, id, displayName string) error
	SetDisabled(ctx context.Context, id string, disabled bool) error
	SetSendOnly(ctx context.Context, id string, sendOnly bool) error
	UpdateUsage(ctx context.Context, id string, usageBytes uint64, at time.Time) error
	ExistsByDomainAndLocalPart(ctx context.Context, domainID, localPart string) (bool, error)
}

type mailboxRepo struct{ db *gorm.DB }

func NewMailboxRepository(db *gorm.DB) MailboxRepository {
	return &mailboxRepo{db: db}
}

func (r *mailboxRepo) FindByID(ctx context.Context, id string) (*models.Mailbox, error) {
	var mb models.Mailbox
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&mb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &mb, nil
}

func (r *mailboxRepo) FindByIDs(ctx context.Context, ids []string) ([]models.Mailbox, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []models.Mailbox
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *mailboxRepo) FindByEmail(ctx context.Context, email string) (*models.Mailbox, error) {
	var mb models.Mailbox
	if err := r.db.WithContext(ctx).Where("email_cached = ?", email).First(&mb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &mb, nil
}

// mailboxListCols — free-text search matches local_part and the cached
// full address so a query for "alice" or "alice@example.com" both hit.
var mailboxListCols = ListCols{
	Search:      []string{"local_part", "email_cached"},
	Sort:        []string{"local_part", "email_cached", "created_at", "quota_bytes", "last_usage_bytes"},
	DefaultSort: "created_at",
}

func (r *mailboxRepo) ListByDomainID(ctx context.Context, domainID string, opts ListOptions) ([]models.Mailbox, int64, error) {
	var (
		rows  []models.Mailbox
		total int64
	)
	base := r.db.WithContext(ctx).Model(&models.Mailbox{}).Where("domain_id = ?", domainID)
	if opts.ExcludeSystem {
		base = base.Where("system = 0") // GH #1056: hide the JAB-230 relay from list + count
	}

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, mailboxListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, mailboxListCols)
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// MailboxWithDomain is a mailbox joined with its domain + owner, for the
// admin server-wide Mail tab (GH #197 companion).
type MailboxWithDomain struct {
	models.Mailbox
	DomainName   string `gorm:"column:domain_name" json:"domain_name"`
	OwnerUserID  string `gorm:"column:owner_user_id" json:"owner_user_id"`
	UserUsername string `gorm:"column:user_username" json:"user_username"`
}

// CountAll returns the number of mailboxes without materialising any rows.
//
// The automation mail summary used to call ListAllWithDomain and take len() of
// the result: a full-table read with two JOINs, pulling every column including
// the bcrypt hash and the AES ciphertext (up to 512 bytes/row), purely to
// produce one integer — on an endpoint a fleet monitor polls.
func (r *mailboxRepo) CountAll(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.Mailbox{}).Count(&n).Error
	return n, err
}

// mailboxInventorySelect is the safe column projection for the WithDomain list
// reads (JAB-370): every mailboxes column EXCEPT the secret-bearing
// password_hash and the AES-encrypted password_enc. The inventory/directory
// views never need either, and `SELECT m.*` was pulling both into memory (bcrypt
// hash + up to 512 bytes of ciphertext per row) on admin- and fleet-polled
// paths. An explicit allowlist also fails safe: a future secret column is not
// materialised unless it is deliberately added here.
const mailboxInventorySelect = "m.id, m.domain_id, m.local_part, m.email_cached, " +
	"m.display_name, m.quota_bytes, m.is_disabled, m.send_only, m.system, " +
	"m.last_usage_bytes, m.last_usage_at, m.created_at, m.updated_at, " +
	"d.name AS domain_name, d.user_id AS owner_user_id, COALESCE(u.username, '') AS user_username"

// ListAllWithDomain returns every mailbox on the server joined with its
// domain name + owning user's username, ordered by email. Admin-only path.
func (r *mailboxRepo) ListAllWithDomain(ctx context.Context) ([]MailboxWithDomain, error) {
	var rows []MailboxWithDomain
	err := r.db.WithContext(ctx).
		Table("mailboxes m").
		Select(mailboxInventorySelect).
		Joins("JOIN domains d ON d.id = m.domain_id").
		Joins("LEFT JOIN users u ON u.id = d.user_id").
		Order("m.email_cached ASC").
		Scan(&rows).Error
	return rows, err
}

// ListByOwnerWithDomain is ListAllWithDomain scoped to a single owner (the
// owning domain's user_id), for the admin owner-scoped Mail view (#483).
func (r *mailboxRepo) ListByOwnerWithDomain(ctx context.Context, userID string) ([]MailboxWithDomain, error) {
	var rows []MailboxWithDomain
	err := r.db.WithContext(ctx).
		Table("mailboxes m").
		Select(mailboxInventorySelect).
		Joins("JOIN domains d ON d.id = m.domain_id").
		Joins("LEFT JOIN users u ON u.id = d.user_id").
		Where("d.user_id = ?", userID).
		Order("m.email_cached ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *mailboxRepo) CountByDomainID(ctx context.Context, domainID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Mailbox{}).Where("domain_id = ?", domainID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// Create inserts a mailbox row. Caller must pre-populate:
//   - ID (ULID)
//   - DomainID
//   - LocalPart
//   - PasswordHash (bcrypt)
//   - QuotaBytes (or rely on the default via the column DEFAULT)
//   - CreatedAt / UpdatedAt
//
// EmailCached does NOT need to be set; the BEFORE INSERT trigger
// computes it as CONCAT(local_part, '@', domain.name). Setting it
// from Go is harmless (the trigger overwrites it anyway), but the
// caller should not RELY on that value.
func (r *mailboxRepo) Create(ctx context.Context, mb *models.Mailbox) error {
	return r.db.WithContext(ctx).Create(mb).Error
}

func (r *mailboxRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.Mailbox{}).Error
}

func (r *mailboxRepo) UpdatePasswordHash(ctx context.Context, id string, hash string) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"password_hash": hash,
			"updated_at":    time.Now().UTC(),
		}).Error
}

// UpdatePasswordHashAndEnc atomically sets both bcrypt hash + plaintext
// cipher envelope. Callers hand in the already-sealed bytes from
// ssokey.Key.Seal; the repository never touches plaintext.
func (r *mailboxRepo) UpdatePasswordHashAndEnc(ctx context.Context, id string, hash string, enc []byte) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"password_hash": hash,
			"password_enc":  enc,
			"updated_at":    time.Now().UTC(),
		}).Error
}

func (r *mailboxRepo) UpdateQuota(ctx context.Context, id string, quotaBytes uint64) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"quota_bytes": quotaBytes,
			"updated_at":  time.Now().UTC(),
		}).Error
}

func (r *mailboxRepo) UpdateDisplayName(ctx context.Context, id, displayName string) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"display_name": displayName,
			"updated_at":   time.Now().UTC(),
		}).Error
}

func (r *mailboxRepo) SetDisabled(ctx context.Context, id string, disabled bool) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"is_disabled": disabled,
			"updated_at":  time.Now().UTC(),
		}).Error
}

// SetSendOnly flips the send-only flag (GH #371). Stalwart's SqlDirectory
// re-reads the row on the next recipient lookup (no cache to invalidate,
// ADR-0045), so a send-only account stops receiving on the next inbound
// delivery attempt with no agent round-trip.
func (r *mailboxRepo) SetSendOnly(ctx context.Context, id string, sendOnly bool) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"send_only":  sendOnly,
			"updated_at": time.Now().UTC(),
		}).Error
}

// UpdateUsage writes back the last observed usage bytes and sample
// time from the reconciler's mailbox.usage probe. Kept separate from
// UpdateQuota so we can lock the reconciler down with a narrower
// grant if we ever split it out.
func (r *mailboxRepo) UpdateUsage(ctx context.Context, id string, usageBytes uint64, at time.Time) error {
	return r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_usage_bytes": usageBytes,
			"last_usage_at":    at.UTC(),
			// updated_at intentionally NOT touched here — usage
			// writebacks shouldn't mark the row as user-edited.
		}).Error
}

func (r *mailboxRepo) ExistsByDomainAndLocalPart(ctx context.Context, domainID, localPart string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Mailbox{}).
		Where("domain_id = ? AND local_part = ?", domainID, localPart).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
