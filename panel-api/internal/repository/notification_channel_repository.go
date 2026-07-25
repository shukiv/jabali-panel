package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// NotificationChannelRepository covers the notification_channels table
// from migration 000064. See ADR-0056 for the dispatcher that consumes
// the enabled rows via FindEnabledByKind + FindEnabledAll.
type NotificationChannelRepository interface {
	Create(ctx context.Context, ch *models.NotificationChannel) error
	Update(ctx context.Context, ch *models.NotificationChannel) error
	Delete(ctx context.Context, id string) error

	FindByID(ctx context.Context, id string) (*models.NotificationChannel, error)
	ListAll(ctx context.Context, opts ListOptions) ([]models.NotificationChannel, int64, error)

	// FindEnabledByKind returns every row with the given kind that has
	// enabled=true. Used by the dispatcher at fanout time to resolve
	// which concrete channels a kind (e.g. "slack") targets.
	FindEnabledByKind(ctx context.Context, kind string) ([]models.NotificationChannel, error)

	// FindEnabledAll returns every enabled channel across kinds. Used
	// by broadcast-on-every-channel event handlers.
	FindEnabledAll(ctx context.Context) ([]models.NotificationChannel, error)

	// FindEnabledServerWide returns every enabled channel that is NOT owned
	// by a tenant (user_id IS NULL) — the admin-configured channels. The
	// dispatcher uses this as the base fan-out set so a tenant-owned channel
	// never receives a broadcast/server event (JAB-171).
	FindEnabledServerWide(ctx context.Context) ([]models.NotificationChannel, error)
}

type notificationChannelRepo struct{ db *gorm.DB }

func NewNotificationChannelRepository(db *gorm.DB) NotificationChannelRepository {
	return &notificationChannelRepo{db: db}
}

func (r *notificationChannelRepo) Create(ctx context.Context, ch *models.NotificationChannel) error {
	return r.db.WithContext(ctx).Create(ch).Error
}

func (r *notificationChannelRepo) Update(ctx context.Context, ch *models.NotificationChannel) error {
	return r.db.WithContext(ctx).Save(ch).Error
}

func (r *notificationChannelRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.NotificationChannel{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *notificationChannelRepo) FindByID(ctx context.Context, id string) (*models.NotificationChannel, error) {
	var row models.NotificationChannel
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// notificationChannelListCols — only name and kind are free-text
// searchable; config_json deliberately NOT searched (would leak
// bearer tokens via SQL error messages in extreme cases, and the
// operator picks channels by name).
var notificationChannelListCols = ListCols{
	Search:      []string{"name", "kind"},
	Sort:        []string{"name", "kind", "created_at", "updated_at", "enabled"},
	DefaultSort: "created_at",
}

func (r *notificationChannelRepo) ListAll(ctx context.Context, opts ListOptions) ([]models.NotificationChannel, int64, error) {
	var rows []models.NotificationChannel
	var total int64
	base := r.db.WithContext(ctx).Model(&models.NotificationChannel{})
	if err := applyListOptions(base, ListOptions{Search: opts.Search}, notificationChannelListCols).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	q := applyListOptions(base, opts, notificationChannelListCols)
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *notificationChannelRepo) FindEnabledByKind(ctx context.Context, kind string) ([]models.NotificationChannel, error) {
	var rows []models.NotificationChannel
	err := r.db.WithContext(ctx).
		Where("kind = ? AND enabled = ?", kind, true).
		Order("id asc").
		Find(&rows).Error
	return rows, err
}

func (r *notificationChannelRepo) FindEnabledAll(ctx context.Context) ([]models.NotificationChannel, error) {
	var rows []models.NotificationChannel
	err := r.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("kind asc, id asc").
		Find(&rows).Error
	return rows, err
}

func (r *notificationChannelRepo) FindEnabledServerWide(ctx context.Context) ([]models.NotificationChannel, error) {
	var rows []models.NotificationChannel
	err := r.db.WithContext(ctx).
		Where("enabled = ? AND user_id IS NULL", true).
		Order("kind asc, id asc").
		Find(&rows).Error
	return rows, err
}
