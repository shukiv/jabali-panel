package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ErrSwitchoverInFlight: a mail hostname switchover attempt is running; the
// request can be neither replaced nor cancelled until it finishes.
var ErrSwitchoverInFlight = errors.New("repository: a mail hostname switchover is in progress")

// ErrSwitchoverChanged: the request no longer asks for the name being
// applied, so the switchover was not completed.
var ErrSwitchoverChanged = errors.New("repository: mail hostname switchover request changed")

// MailHostnameSwitchoverRepository stores the JAB-390 switchover request
// (singleton, migration 000305) and completes a switchover.
type MailHostnameSwitchoverRepository interface {
	// Get returns the request row; ErrNotFound when none was ever made.
	Get(ctx context.Context) (*models.MailHostnameSwitchover, error)
	// Request records desired as a pending request, replacing any earlier
	// one. ErrSwitchoverInFlight while an attempt is issuing.
	Request(ctx context.Context, desired, requestedBy string, now time.Time) error
	// Cancel withdraws a pending or failed request. ErrSwitchoverInFlight
	// while an attempt is issuing; ErrNotFound when there is nothing to
	// cancel.
	Cancel(ctx context.Context, now time.Time) error
	// Claim marks the attempt for desired as issuing. It claims only a row
	// that still asks for desired and is due: pending, failed with its retry
	// time reached, or issuing but not updated since staleBefore (an attempt
	// that died). false means nothing was claimed.
	Claim(ctx context.Context, desired string, staleBefore, now time.Time) (bool, error)
	// Fail records a failed attempt for desired, retried at retryAt.
	Fail(ctx context.Context, desired, msg string, retryAt, now time.Time) error
	// Complete applies desired in one transaction: server_settings.mail_hostname
	// becomes applied (nil = the derived mail.<hostname>), the mail
	// certificate row moves to desired with the issued dates, and the request
	// is marked done. It rolls back with ErrSwitchoverChanged when the
	// request no longer asks for desired.
	Complete(ctx context.Context, desired string, applied *string, issuedAt, expiresAt, now time.Time) error
}

type mailHostnameSwitchoverRepo struct{ db *gorm.DB }

// NewMailHostnameSwitchoverRepository returns a repository backed by db.
func NewMailHostnameSwitchoverRepository(db *gorm.DB) MailHostnameSwitchoverRepository {
	return &mailHostnameSwitchoverRepo{db: db}
}

func (r *mailHostnameSwitchoverRepo) Get(ctx context.Context) (*models.MailHostnameSwitchover, error) {
	var row models.MailHostnameSwitchover
	err := r.db.WithContext(ctx).Where("id = ?", 1).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *mailHostnameSwitchoverRepo) Request(ctx context.Context, desired, requestedBy string, now time.Time) error {
	now = now.UTC()
	res := r.db.WithContext(ctx).
		Model(&models.MailHostnameSwitchover{}).
		Where("id = ? AND status <> ?", 1, models.MailHostnameSwitchoverIssuing).
		Updates(map[string]any{
			"desired":       desired,
			"status":        models.MailHostnameSwitchoverPending,
			"last_error":    "",
			"attempts":      0,
			"next_retry_at": nil,
			"requested_by":  requestedBy,
			"requested_at":  now,
			"updated_at":    now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	existing, err := r.Get(ctx)
	switch {
	case err == nil && existing.Status == models.MailHostnameSwitchoverIssuing:
		return ErrSwitchoverInFlight
	case err == nil:
		// The row matched but nothing changed (same values in the same
		// millisecond) — the request is recorded.
		return nil
	case !errors.Is(err, ErrNotFound):
		return err
	}
	return r.db.WithContext(ctx).Create(&models.MailHostnameSwitchover{
		ID:          1,
		Desired:     &desired,
		Status:      models.MailHostnameSwitchoverPending,
		RequestedBy: requestedBy,
		RequestedAt: &now,
		UpdatedAt:   now,
	}).Error
}

func (r *mailHostnameSwitchoverRepo) Cancel(ctx context.Context, now time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&models.MailHostnameSwitchover{}).
		Where("id = ? AND status IN (?,?)", 1, models.MailHostnameSwitchoverPending, models.MailHostnameSwitchoverFailed).
		Updates(map[string]any{
			"desired":       nil,
			"status":        models.MailHostnameSwitchoverIdle,
			"last_error":    "",
			"next_retry_at": nil,
			"updated_at":    now.UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	existing, err := r.Get(ctx)
	if err != nil {
		return err
	}
	if existing.Status == models.MailHostnameSwitchoverIssuing {
		return ErrSwitchoverInFlight
	}
	return ErrNotFound
}

func (r *mailHostnameSwitchoverRepo) Claim(ctx context.Context, desired string, staleBefore, now time.Time) (bool, error) {
	now = now.UTC()
	res := r.db.WithContext(ctx).
		Model(&models.MailHostnameSwitchover{}).
		Where("id = ? AND desired = ? AND (status = ? OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (status = ? AND updated_at < ?))",
			1, desired,
			models.MailHostnameSwitchoverPending,
			models.MailHostnameSwitchoverFailed, now,
			models.MailHostnameSwitchoverIssuing, staleBefore.UTC()).
		Updates(map[string]any{
			"status":     models.MailHostnameSwitchoverIssuing,
			"attempts":   gorm.Expr("attempts + 1"),
			"updated_at": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (r *mailHostnameSwitchoverRepo) Fail(ctx context.Context, desired, msg string, retryAt, now time.Time) error {
	if len(msg) > 1024 {
		msg = msg[:1021] + "..."
	}
	return r.db.WithContext(ctx).
		Model(&models.MailHostnameSwitchover{}).
		Where("id = ? AND desired = ?", 1, desired).
		Updates(map[string]any{
			"status":        models.MailHostnameSwitchoverFailed,
			"last_error":    msg,
			"next_retry_at": retryAt.UTC(),
			"updated_at":    now.UTC(),
		}).Error
}

func (r *mailHostnameSwitchoverRepo) Complete(ctx context.Context, desired string, applied *string, issuedAt, expiresAt, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ServerSettings{}).
			Where("id = ?", 1).
			Update("mail_hostname", applied).Error; err != nil {
			return fmt.Errorf("apply mail hostname: %w", err)
		}
		if err := tx.Model(&models.PanelCertificate{}).
			Where("kind = ?", models.PanelCertKindMail).
			Updates(map[string]any{
				"hostname":      desired,
				"status":        models.PanelCertStatusIssued,
				"issued_at":     issuedAt.UTC(),
				"expires_at":    expiresAt.UTC(),
				"last_error":    "",
				"attempt_count": 0,
				"next_retry_at": nil,
			}).Error; err != nil {
			return fmt.Errorf("move mail certificate row: %w", err)
		}
		res := tx.Model(&models.MailHostnameSwitchover{}).
			Where("id = ? AND desired = ?", 1, desired).
			Updates(map[string]any{
				"status":        models.MailHostnameSwitchoverDone,
				"last_error":    "",
				"next_retry_at": nil,
				"updated_at":    now.UTC(),
			})
		if res.Error != nil {
			return fmt.Errorf("mark switchover done: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrSwitchoverChanged
		}
		return nil
	})
}
