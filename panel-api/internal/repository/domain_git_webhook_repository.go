package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainGitWebhookRepository persists domains' deploy-on-push webhooks (GH
// #2277, ADR-0178).
type DomainGitWebhookRepository interface {
	// Get returns the domain's webhook; ErrNotFound when it has none.
	Get(ctx context.Context, domainID string) (*models.DomainGitWebhook, error)
	// FindByHookID returns the webhook a delivery's URL names; ErrNotFound
	// when there is none.
	FindByHookID(ctx context.Context, hookID string) (*models.DomainGitWebhook, error)
	// Put stores the webhook's key and secret. What deliveries recorded on
	// an existing webhook is kept.
	Put(ctx context.Context, row *models.DomainGitWebhook) error
	// Delete removes the domain's webhook; ErrNotFound when it has none.
	Delete(ctx context.Context, domainID string) error
	// Queue records that a push waits for the domain's running deployment to
	// end. A push already waiting keeps its time.
	Queue(ctx context.Context, domainID string, at time.Time) error
	// ListQueued returns the webhooks with a push waiting, oldest first.
	ListQueued(ctx context.Context, limit int) ([]models.DomainGitWebhook, error)
	// Unqueue takes the waiting push. It reports false when none was
	// waiting: another pass took it, or the webhook is gone.
	Unqueue(ctx context.Context, domainID string) (bool, error)
	// RecordDelivery stores when the last delivery came and what it led to.
	// A zero at keeps the time: a waiting push that started later.
	RecordDelivery(ctx context.Context, domainID string, at time.Time, result string) error
}

type domainGitWebhookRepo struct{ db *gorm.DB }

func NewDomainGitWebhookRepository(db *gorm.DB) DomainGitWebhookRepository {
	return &domainGitWebhookRepo{db: db}
}

func (r *domainGitWebhookRepo) find(ctx context.Context, column, value string) (*models.DomainGitWebhook, error) {
	var row models.DomainGitWebhook
	err := r.db.WithContext(ctx).Where(column+" = ?", value).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *domainGitWebhookRepo) Get(ctx context.Context, domainID string) (*models.DomainGitWebhook, error) {
	return r.find(ctx, "domain_id", domainID)
}

func (r *domainGitWebhookRepo) FindByHookID(ctx context.Context, hookID string) (*models.DomainGitWebhook, error) {
	return r.find(ctx, "hook_id", hookID)
}

func (r *domainGitWebhookRepo) Put(ctx context.Context, row *models.DomainGitWebhook) error {
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"hook_id", "secret_enc", "updated_at"}),
	}).Create(row).Error
	return translate(err)
}

func (r *domainGitWebhookRepo) Delete(ctx context.Context, domainID string) error {
	res := r.db.WithContext(ctx).Where("domain_id = ?", domainID).Delete(&models.DomainGitWebhook{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainGitWebhookRepo) Queue(ctx context.Context, domainID string, at time.Time) error {
	return r.db.WithContext(ctx).Model(&models.DomainGitWebhook{}).
		Where("domain_id = ? AND queued_at IS NULL", domainID).
		Updates(map[string]any{"queued_at": at, "updated_at": time.Now().UTC()}).Error
}

func (r *domainGitWebhookRepo) ListQueued(ctx context.Context, limit int) ([]models.DomainGitWebhook, error) {
	rows := []models.DomainGitWebhook{}
	err := r.db.WithContext(ctx).Where("queued_at IS NOT NULL").
		Order("queued_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *domainGitWebhookRepo) Unqueue(ctx context.Context, domainID string) (bool, error) {
	// One statement: of two passes, only one takes the push.
	res := r.db.WithContext(ctx).Model(&models.DomainGitWebhook{}).
		Where("domain_id = ? AND queued_at IS NOT NULL", domainID).
		Updates(map[string]any{"queued_at": nil, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (r *domainGitWebhookRepo) RecordDelivery(ctx context.Context, domainID string, at time.Time, result string) error {
	if len(result) > 64 {
		result = result[:64]
	}
	set := map[string]any{"last_result": result, "updated_at": time.Now().UTC()}
	if !at.IsZero() {
		set["last_delivery_at"] = at
	}
	return r.db.WithContext(ctx).Model(&models.DomainGitWebhook{}).
		Where("domain_id = ?", domainID).Updates(set).Error
}
