package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// EmailAutoresponderRepository defines data access for autoresponse/vacation messages.
// Stalwart integration: JMAP VacationResponse (RFC 8621 §8).
// Jabali is truth; reconciler converges to Stalwart (ADR-0051).
type EmailAutoresponderRepository interface {
	FindByMailboxID(ctx context.Context, mailboxID string) (*models.EmailAutoresponder, error)
	Update(ctx context.Context, autoresponder *models.EmailAutoresponder) error
	Delete(ctx context.Context, mailboxID string) error

	// ListByDomain returns every autoresponder row for the domain's
	// mailboxes (GH #240 — the Mailboxes tab "Auto replies" column fans
	// out one bulk call per domain instead of one GET per mailbox).
	ListByDomain(ctx context.Context, domainID string) ([]models.EmailAutoresponder, error)

	// ListAll returns every autoresponder row server-wide (JAB-76 — the
	// read-only automation mail inventory a fleet manager reads).
	ListAll(ctx context.Context) ([]models.EmailAutoresponder, error)
}

type emailAutoresponderRepo struct {
	db *gorm.DB
}

// NewEmailAutoresponderRepository returns the GORM-backed impl.
func NewEmailAutoresponderRepository(db *gorm.DB) EmailAutoresponderRepository {
	return &emailAutoresponderRepo{db: db}
}

func (r *emailAutoresponderRepo) FindByMailboxID(ctx context.Context, mailboxID string) (*models.EmailAutoresponder, error) {
	var ar models.EmailAutoresponder
	if err := r.db.WithContext(ctx).Where("mailbox_id = ?", mailboxID).First(&ar).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ar, nil
}

func (r *emailAutoresponderRepo) Update(ctx context.Context, autoresponder *models.EmailAutoresponder) error {
	autoresponder.UpdatedAt = time.Now().UTC()
	// Upsert by PK (mailbox_id).
	return r.db.WithContext(ctx).Save(autoresponder).Error
}

func (r *emailAutoresponderRepo) Delete(ctx context.Context, mailboxID string) error {
	res := r.db.WithContext(ctx).Delete(&models.EmailAutoresponder{}, "mailbox_id = ?", mailboxID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *emailAutoresponderRepo) ListByDomain(ctx context.Context, domainID string) ([]models.EmailAutoresponder, error) {
	var rows []models.EmailAutoresponder
	err := r.db.WithContext(ctx).
		Table("email_autoresponders a").
		Select("a.*").
		Joins("JOIN mailboxes m ON m.id = a.mailbox_id").
		Where("m.domain_id = ?", domainID).
		Find(&rows).Error
	return rows, err
}

func (r *emailAutoresponderRepo) ListAll(ctx context.Context) ([]models.EmailAutoresponder, error) {
	var rows []models.EmailAutoresponder
	err := r.db.WithContext(ctx).Order("mailbox_id").Find(&rows).Error
	return rows, err
}
