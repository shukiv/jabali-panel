package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MailboxTrustedSenderRepository is data access for the senders a mailbox
// trusts (GH #2017). The rows are the truth (ADR-0042); the reconciler writes
// each mailbox's list into Stalwart.
type MailboxTrustedSenderRepository interface {
	// Create inserts a row. The same address twice on a mailbox is ErrConflict.
	Create(ctx context.Context, row *models.MailboxTrustedSender) error
	// Delete removes the mailbox's row id; ErrNotFound when the mailbox has
	// no such row.
	Delete(ctx context.Context, mailboxID, id string) error
	// ListByMailbox returns a mailbox's rows, by address.
	ListByMailbox(ctx context.Context, mailboxID string) ([]models.MailboxTrustedSender, error)
	// ListByMailboxIDs returns the rows of every listed mailbox (the backup).
	ListByMailboxIDs(ctx context.Context, mailboxIDs []string) ([]models.MailboxTrustedSender, error)
	// ListAll returns every row, by mailbox and address (the reconciler).
	ListAll(ctx context.Context) ([]models.MailboxTrustedSender, error)
	CountByMailbox(ctx context.Context, mailboxID string) (int64, error)
}

type mailboxTrustedSenderRepo struct {
	db *gorm.DB
}

func NewMailboxTrustedSenderRepository(db *gorm.DB) MailboxTrustedSenderRepository {
	return &mailboxTrustedSenderRepo{db: db}
}

func (r *mailboxTrustedSenderRepo) Create(ctx context.Context, row *models.MailboxTrustedSender) error {
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	return translate(r.db.WithContext(ctx).Create(row).Error)
}

func (r *mailboxTrustedSenderRepo) Delete(ctx context.Context, mailboxID, id string) error {
	res := r.db.WithContext(ctx).Where("mailbox_id = ? AND id = ?", mailboxID, id).Delete(&models.MailboxTrustedSender{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *mailboxTrustedSenderRepo) ListByMailbox(ctx context.Context, mailboxID string) ([]models.MailboxTrustedSender, error) {
	var rows []models.MailboxTrustedSender
	if err := r.db.WithContext(ctx).Where("mailbox_id = ?", mailboxID).Order("address ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *mailboxTrustedSenderRepo) ListByMailboxIDs(ctx context.Context, mailboxIDs []string) ([]models.MailboxTrustedSender, error) {
	if len(mailboxIDs) == 0 {
		return nil, nil
	}
	var rows []models.MailboxTrustedSender
	if err := r.db.WithContext(ctx).Where("mailbox_id IN ?", mailboxIDs).Order("mailbox_id ASC, address ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *mailboxTrustedSenderRepo) ListAll(ctx context.Context) ([]models.MailboxTrustedSender, error) {
	var rows []models.MailboxTrustedSender
	if err := r.db.WithContext(ctx).Order("mailbox_id ASC, address ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *mailboxTrustedSenderRepo) CountByMailbox(ctx context.Context, mailboxID string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&models.MailboxTrustedSender{}).Where("mailbox_id = ?", mailboxID).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}
