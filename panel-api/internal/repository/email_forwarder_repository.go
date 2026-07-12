package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// EmailForwarderRepository defines data access for email forwarders (aliases + external forwards).
// Stalwart integration: x:UserAccount.aliases + x:SieveUserScript.
// Jabali is truth; reconciler converges to Stalwart (ADR-0051).
type EmailForwarderRepository interface {
	FindByID(ctx context.Context, id string) (*models.EmailForwarder, error)
	ListByDomainID(ctx context.Context, domainID string, opts ListOptions) ([]models.EmailForwarder, int64, error)
	ListByMailboxID(ctx context.Context, mailboxID string, opts ListOptions) ([]models.EmailForwarder, int64, error)
	ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.EmailForwarder, int64, error)
	ListAll(ctx context.Context, opts ListOptions) ([]models.EmailForwarder, int64, error)
	Create(ctx context.Context, fwd *models.EmailForwarder) error
	Update(ctx context.Context, fwd *models.EmailForwarder) error
	Delete(ctx context.Context, id string) error
}

type emailForwarderRepo struct {
	db *gorm.DB
}

func NewEmailForwarderRepository(db *gorm.DB) EmailForwarderRepository {
	return &emailForwarderRepo{db: db}
}

func (r *emailForwarderRepo) FindByID(ctx context.Context, id string) (*models.EmailForwarder, error) {
	var f models.EmailForwarder
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

func (r *emailForwarderRepo) listByField(ctx context.Context, field, value string, opts ListOptions) ([]models.EmailForwarder, int64, error) {
	var rows []models.EmailForwarder
	var total int64
	q := r.db.WithContext(ctx).Model(&models.EmailForwarder{}).Where(field+" = ?", value)
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
	if err := tx.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *emailForwarderRepo) ListByDomainID(ctx context.Context, domainID string, opts ListOptions) ([]models.EmailForwarder, int64, error) {
	return r.listByField(ctx, "domain_id", domainID, opts)
}

func (r *emailForwarderRepo) ListByMailboxID(ctx context.Context, mailboxID string, opts ListOptions) ([]models.EmailForwarder, int64, error) {
	return r.listByField(ctx, "mailbox_id", mailboxID, opts)
}

// ListByUserID returns the forwarders owned by one user — those whose domain
// belongs to the user — scoped entirely in SQL (JOIN domains) so a tenant
// always sees all of their own entries regardless of the global row count
// (JAB-107). Only mailbox-keyed forwarders are returned: domain-scoped
// DA-imported rows have no mailbox and the M65 mailbox-keyed UI cannot render
// them, so `total` matches exactly what the caller displays.
func (r *emailForwarderRepo) ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.EmailForwarder, int64, error) {
	var rows []models.EmailForwarder
	var total int64
	q := r.db.WithContext(ctx).Model(&models.EmailForwarder{}).
		Joins("JOIN domains ON domains.id = email_forwarders.domain_id").
		Where("domains.user_id = ? AND email_forwarders.mailbox_id IS NOT NULL", userID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx := q.Select("email_forwarders.*").Order("email_forwarders.created_at DESC")
	if opts.Limit > 0 {
		tx = tx.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		tx = tx.Offset(opts.Offset)
	}
	if err := tx.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *emailForwarderRepo) ListAll(ctx context.Context, opts ListOptions) ([]models.EmailForwarder, int64, error) {
	var rows []models.EmailForwarder
	var total int64
	q := r.db.WithContext(ctx).Model(&models.EmailForwarder{})
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
	if err := tx.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *emailForwarderRepo) Create(ctx context.Context, fwd *models.EmailForwarder) error {
	now := time.Now().UTC()
	fwd.CreatedAt = now
	fwd.UpdatedAt = now
	return r.db.WithContext(ctx).Create(fwd).Error
}

func (r *emailForwarderRepo) Update(ctx context.Context, fwd *models.EmailForwarder) error {
	fwd.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Save(fwd).Error
}

func (r *emailForwarderRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.EmailForwarder{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
