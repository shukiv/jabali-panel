package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// UserCloudflareTokenRepository is data access for tenants' own Cloudflare
// API tokens (GH #2032). The token is stored sealed; this layer never sees
// the plaintext.
type UserCloudflareTokenRepository interface {
	// Get returns the user's token row; ErrNotFound when the user has none.
	Get(ctx context.Context, userID string) (*models.UserCloudflareToken, error)
	// Put stores the user's token, replacing an earlier one.
	Put(ctx context.Context, row *models.UserCloudflareToken) error
	// Delete removes the user's token; ErrNotFound when there is none.
	Delete(ctx context.Context, userID string) error
}

type userCloudflareTokenRepo struct {
	db *gorm.DB
}

func NewUserCloudflareTokenRepository(db *gorm.DB) UserCloudflareTokenRepository {
	return &userCloudflareTokenRepo{db: db}
}

func (r *userCloudflareTokenRepo) Get(ctx context.Context, userID string) (*models.UserCloudflareToken, error) {
	var row models.UserCloudflareToken
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&row).Error; err != nil {
		return nil, translate(err)
	}
	return &row, nil
}

func (r *userCloudflareTokenRepo) Put(ctx context.Context, row *models.UserCloudflareToken) error {
	row.UpdatedAt = time.Now().UTC()
	return translate(r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"token_enc", "verified_at", "updated_at"}),
	}).Create(row).Error)
}

func (r *userCloudflareTokenRepo) Delete(ctx context.Context, userID string) error {
	res := r.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&models.UserCloudflareToken{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
