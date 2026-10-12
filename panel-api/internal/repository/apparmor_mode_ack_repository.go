package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// AppArmorModeAckRepository stores administrators' acknowledgements of AppArmor
// profiles kept in complain mode on purpose (GH #2078).
type AppArmorModeAckRepository interface {
	// List returns every acknowledgement.
	List(ctx context.Context) ([]models.AppArmorModeAck, error)
	// Put stores the acknowledgement for its profile, replacing an earlier one.
	Put(ctx context.Context, row *models.AppArmorModeAck) error
	// Delete removes the profile's acknowledgement. A profile without one is
	// not an error: callers delete whenever a profile's mode changes.
	Delete(ctx context.Context, profile string) error
}

type appArmorModeAckRepo struct{ db *gorm.DB }

func NewAppArmorModeAckRepository(db *gorm.DB) AppArmorModeAckRepository {
	return &appArmorModeAckRepo{db: db}
}

func (r *appArmorModeAckRepo) List(ctx context.Context) ([]models.AppArmorModeAck, error) {
	var out []models.AppArmorModeAck
	err := r.db.WithContext(ctx).Order("profile asc").Find(&out).Error
	return out, err
}

func (r *appArmorModeAckRepo) Put(ctx context.Context, row *models.AppArmorModeAck) error {
	return translate(r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "profile"}},
		DoUpdates: clause.AssignmentColumns([]string{"mode", "acknowledged_by", "acknowledged_at"}),
	}).Create(row).Error)
}

func (r *appArmorModeAckRepo) Delete(ctx context.Context, profile string) error {
	return r.db.WithContext(ctx).Where("profile = ?", profile).Delete(&models.AppArmorModeAck{}).Error
}
