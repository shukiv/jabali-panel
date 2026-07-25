package repository

import (
	"context"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// UserNotificationRouteRepository is the data access for JAB-171 per-user event
// routing: which of a tenant's own channels a given event kind is delivered to.
// Nothing consumes it until phase 2 (recipient-aware dispatcher) — it is added
// here as phase-1 scaffolding so the model + table have a typed surface.
type UserNotificationRouteRepository interface {
	Create(ctx context.Context, r *models.UserNotificationRoute) error
	// ListByUser returns every route a user has configured.
	ListByUser(ctx context.Context, userID string) ([]models.UserNotificationRoute, error)
	// ListByUserEvent returns a user's routes for one event kind (the channels
	// that event fans out to for that user).
	ListByUserEvent(ctx context.Context, userID, eventKind string) ([]models.UserNotificationRoute, error)
	Delete(ctx context.Context, id string) error
	// DeleteByChannel removes every route pointing at a channel (used when a
	// channel is deleted — belt-and-suspenders alongside the ON DELETE CASCADE).
	DeleteByChannel(ctx context.Context, channelID string) error
}

type userNotificationRouteRepo struct{ db *gorm.DB }

func NewUserNotificationRouteRepository(db *gorm.DB) UserNotificationRouteRepository {
	return &userNotificationRouteRepo{db: db}
}

func (r *userNotificationRouteRepo) Create(ctx context.Context, route *models.UserNotificationRoute) error {
	return r.db.WithContext(ctx).Create(route).Error
}

func (r *userNotificationRouteRepo) ListByUser(ctx context.Context, userID string) ([]models.UserNotificationRoute, error) {
	var out []models.UserNotificationRoute
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("event_kind ASC").Find(&out).Error
	return out, err
}

func (r *userNotificationRouteRepo) ListByUserEvent(ctx context.Context, userID, eventKind string) ([]models.UserNotificationRoute, error) {
	var out []models.UserNotificationRoute
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND event_kind = ?", userID, eventKind).
		Find(&out).Error
	return out, err
}

func (r *userNotificationRouteRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.UserNotificationRoute{}).Error
}

func (r *userNotificationRouteRepo) DeleteByChannel(ctx context.Context, channelID string) error {
	return r.db.WithContext(ctx).Where("channel_id = ?", channelID).Delete(&models.UserNotificationRoute{}).Error
}
