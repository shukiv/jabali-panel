package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MailboxShareRepository defines data access for mailbox ACL sharing relationships.
// Stalwart integration: JMAP Mailbox/set + shareWith patch.
// Jabali is truth; reconciler converges to Stalwart (ADR-0051).
type MailboxShareRepository interface {
	FindByID(ctx context.Context, id string) (*models.MailboxShare, error)
	FindByOwnerID(ctx context.Context, ownerMailboxID string, opts ListOptions) ([]models.MailboxShare, int64, error)
	FindBySharedWithID(ctx context.Context, sharedWithMailboxID string, opts ListOptions) ([]models.MailboxShare, int64, error)
	ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.MailboxShare, int64, error)
	ListAll(ctx context.Context, opts ListOptions) ([]models.MailboxShare, int64, error)
	Create(ctx context.Context, share *models.MailboxShare) error
	Update(ctx context.Context, share *models.MailboxShare) error
	Delete(ctx context.Context, id string) error
	// DeleteByOwner is the owner-scoped delete the HTTP layer must use; see
	// the implementation for why the scope belongs in the query.
	DeleteByOwner(ctx context.Context, id, ownerMailboxID string) error
}

type mailboxShareRepo struct {
	db *gorm.DB
}

func NewMailboxShareRepository(db *gorm.DB) MailboxShareRepository {
	return &mailboxShareRepo{db: db}
}

func (r *mailboxShareRepo) FindByID(ctx context.Context, id string) (*models.MailboxShare, error) {
	var s models.MailboxShare
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *mailboxShareRepo) listByField(ctx context.Context, field, value string, opts ListOptions) ([]models.MailboxShare, int64, error) {
	var shares []models.MailboxShare
	var total int64
	q := r.db.WithContext(ctx).Model(&models.MailboxShare{}).Where(field+" = ?", value)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx := q.Order("created_at DESC")
	if opts.Limit > 0 {
		tx = tx.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		tx = tx.Offset(opts.Offset)
	}
	if err := tx.Find(&shares).Error; err != nil {
		return nil, 0, err
	}
	return shares, total, nil
}

func (r *mailboxShareRepo) FindByOwnerID(ctx context.Context, ownerMailboxID string, opts ListOptions) ([]models.MailboxShare, int64, error) {
	return r.listByField(ctx, "owner_mailbox_id", ownerMailboxID, opts)
}

func (r *mailboxShareRepo) FindBySharedWithID(ctx context.Context, sharedWithMailboxID string, opts ListOptions) ([]models.MailboxShare, int64, error) {
	return r.listByField(ctx, "shared_with_mailbox_id", sharedWithMailboxID, opts)
}

// ListByUserID returns the shares owned by one user — those whose owner mailbox
// lives in a domain the user owns — scoped in SQL (mailbox_shares -> mailboxes
// -> domains) so a tenant always sees all of their own shares regardless of the
// global row count (JAB-107).
func (r *mailboxShareRepo) ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.MailboxShare, int64, error) {
	var shares []models.MailboxShare
	var total int64
	q := r.db.WithContext(ctx).Model(&models.MailboxShare{}).
		Joins("JOIN mailboxes ON mailboxes.id = mailbox_shares.owner_mailbox_id").
		Joins("JOIN domains ON domains.id = mailboxes.domain_id").
		Where("domains.user_id = ?", userID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx := q.Select("mailbox_shares.*").Order("mailbox_shares.created_at DESC")
	if opts.Limit > 0 {
		tx = tx.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		tx = tx.Offset(opts.Offset)
	}
	if err := tx.Find(&shares).Error; err != nil {
		return nil, 0, err
	}
	return shares, total, nil
}

func (r *mailboxShareRepo) ListAll(ctx context.Context, opts ListOptions) ([]models.MailboxShare, int64, error) {
	var shares []models.MailboxShare
	var total int64
	q := r.db.WithContext(ctx).Model(&models.MailboxShare{})
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx := q.Order("created_at DESC")
	if opts.Limit > 0 {
		tx = tx.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		tx = tx.Offset(opts.Offset)
	}
	if err := tx.Find(&shares).Error; err != nil {
		return nil, 0, err
	}
	return shares, total, nil
}

func (r *mailboxShareRepo) Create(ctx context.Context, share *models.MailboxShare) error {
	share.CreatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Create(share).Error
}

func (r *mailboxShareRepo) Update(ctx context.Context, share *models.MailboxShare) error {
	return r.db.WithContext(ctx).Save(share).Error
}

func (r *mailboxShareRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.MailboxShare{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteByOwner removes a share only when it belongs to ownerMailboxID,
// returning ErrNotFound otherwise.
//
// The owner scope is part of the DELETE rather than a separate read-then-check
// so the authorization can't be raced, and so no caller can forget it. The
// route is DELETE /mailboxes/:mbid/shares/:shareId: authenticating :mbid alone
// and then deleting by bare :shareId let any authenticated tenant delete
// another tenant's share row by passing their own mailbox as :mbid. Mirrors
// MailboxSendDelegation.DeleteByPair, which had this right.
func (r *mailboxShareRepo) DeleteByOwner(ctx context.Context, id, ownerMailboxID string) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND owner_mailbox_id = ?", id, ownerMailboxID).
		Delete(&models.MailboxShare{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
