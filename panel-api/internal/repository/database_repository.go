package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DatabaseRepository defines data access for hosted databases.
type DatabaseRepository interface {
	FindByID(ctx context.Context, id string) (*models.Database, error)
	List(ctx context.Context, opts ListOptions) ([]models.Database, int64, error)
	ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.Database, int64, error)
	CountByUserID(ctx context.Context, userID string) (int64, error)
	Create(ctx context.Context, d *models.Database) error
	Delete(ctx context.Context, id string) error
	ExistsByUserAndName(ctx context.Context, userID, name string) (bool, error)
	// UpdateName renames the database row in place (GH #1238 DB re-prefix). The
	// on-disk rename is done by the agent; this repoints the panel row.
	UpdateName(ctx context.Context, id, name string) error
}

type databaseRepo struct{ db *gorm.DB }

func NewDatabaseRepository(db *gorm.DB) DatabaseRepository {
	return &databaseRepo{db: db}
}

func (r *databaseRepo) FindByID(ctx context.Context, id string) (*models.Database, error) {
	var d models.Database
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// databaseListCols — only the database name is free-text searchable.
// user_id is deliberately absent from Search because it's an opaque ULID
// and admins have a dedicated "this user's databases" path via ListByUserID.
var databaseListCols = ListCols{
	Search:      []string{"name"},
	Sort:        []string{"name", "created_at"},
	DefaultSort: "created_at",
}

func (r *databaseRepo) List(ctx context.Context, opts ListOptions) ([]models.Database, int64, error) {
	var (
		databases []models.Database
		total     int64
	)
	base := r.db.WithContext(ctx).Model(&models.Database{})

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, databaseListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, databaseListCols)
	if err := q.Find(&databases).Error; err != nil {
		return nil, 0, err
	}
	return databases, total, nil
}

func (r *databaseRepo) ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.Database, int64, error) {
	var (
		databases []models.Database
		total     int64
	)
	base := r.db.WithContext(ctx).Model(&models.Database{}).Where("user_id = ?", userID)

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, databaseListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, databaseListCols)
	if err := q.Find(&databases).Error; err != nil {
		return nil, 0, err
	}
	return databases, total, nil
}

func (r *databaseRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Database{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *databaseRepo) Create(ctx context.Context, d *models.Database) error {
	if err := r.db.WithContext(ctx).Create(d).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *databaseRepo) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Delete(&models.Database{}, "id = ?", id)
	if err := result.Error; err != nil {
		return err
	}
	// Check if any row was actually deleted
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *databaseRepo) UpdateName(ctx context.Context, id, name string) error {
	return translate(r.db.WithContext(ctx).
		Model(&models.Database{}).Where("id = ?", id).Update("name", name).Error)
}

func (r *databaseRepo) ExistsByUserAndName(ctx context.Context, userID, name string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Database{}).Where("user_id = ? AND name = ?", userID, name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
