package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DatabaseUserGrantRepository defines data access for database user grants.
type DatabaseUserGrantRepository interface {
	FindByID(ctx context.Context, id string) (*models.DatabaseUserGrant, error)
	ListByDatabaseID(ctx context.Context, databaseID string) ([]models.DatabaseUserGrant, error)
	ListByDatabaseUserID(ctx context.Context, databaseUserID string) ([]models.DatabaseUserGrant, error)
	ListByDatabaseUserIDs(ctx context.Context, databaseUserIDs []string) ([]models.DatabaseUserGrant, error)
	Create(ctx context.Context, grant *models.DatabaseUserGrant) error
	Delete(ctx context.Context, id string) error
	UpdateLevel(ctx context.Context, id string, level string) error
	UpdatePrivileges(ctx context.Context, id string, privileges string) error
	FindByDBAndDBUser(ctx context.Context, databaseID string, databaseUserID string) (*models.DatabaseUserGrant, error)
}

type databaseUserGrantRepo struct{ db *gorm.DB }

func NewDatabaseUserGrantRepository(db *gorm.DB) DatabaseUserGrantRepository {
	return &databaseUserGrantRepo{db: db}
}

func (r *databaseUserGrantRepo) FindByID(ctx context.Context, id string) (*models.DatabaseUserGrant, error) {
	var g models.DatabaseUserGrant
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

func (r *databaseUserGrantRepo) ListByDatabaseID(ctx context.Context, databaseID string) ([]models.DatabaseUserGrant, error) {
	var grants []models.DatabaseUserGrant
	if err := r.db.WithContext(ctx).Where("database_id = ?", databaseID).Find(&grants).Error; err != nil {
		return nil, err
	}
	return grants, nil
}

func (r *databaseUserGrantRepo) ListByDatabaseUserID(ctx context.Context, databaseUserID string) ([]models.DatabaseUserGrant, error) {
	var grants []models.DatabaseUserGrant
	if err := r.db.WithContext(ctx).Where("database_user_id = ?", databaseUserID).Find(&grants).Error; err != nil {
		return nil, err
	}
	return grants, nil
}

// ListByDatabaseUserIDs batch-fetches grants for a list of user ids —
// used by the list handler to avoid a per-row query while building the
// grants-embedded response. An empty input returns an empty slice
// without hitting the DB.
func (r *databaseUserGrantRepo) ListByDatabaseUserIDs(ctx context.Context, databaseUserIDs []string) ([]models.DatabaseUserGrant, error) {
	if len(databaseUserIDs) == 0 {
		return []models.DatabaseUserGrant{}, nil
	}
	var grants []models.DatabaseUserGrant
	if err := r.db.WithContext(ctx).Where("database_user_id IN ?", databaseUserIDs).Find(&grants).Error; err != nil {
		return nil, err
	}
	return grants, nil
}

func (r *databaseUserGrantRepo) Create(ctx context.Context, grant *models.DatabaseUserGrant) error {
	return translate(r.db.WithContext(ctx).Create(grant).Error)
}

func (r *databaseUserGrantRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.DatabaseUserGrant{}).Error
}

func (r *databaseUserGrantRepo) UpdateLevel(ctx context.Context, id string, level string) error {
	return r.db.WithContext(ctx).Model(&models.DatabaseUserGrant{}).Where("id = ?", id).Update("grant_level", level).Error
}

func (r *databaseUserGrantRepo) UpdatePrivileges(ctx context.Context, id string, privileges string) error {
	return r.db.WithContext(ctx).Model(&models.DatabaseUserGrant{}).Where("id = ?", id).Updates(map[string]interface{}{
		"privileges":   privileges,
		"grant_level":  "custom", // Mark as custom when using privilege granularity
	}).Error
}

func (r *databaseUserGrantRepo) FindByDBAndDBUser(ctx context.Context, databaseID string, databaseUserID string) (*models.DatabaseUserGrant, error) {
	var g models.DatabaseUserGrant
	if err := r.db.WithContext(ctx).Where("database_id = ? AND database_user_id = ?", databaseID, databaseUserID).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}
