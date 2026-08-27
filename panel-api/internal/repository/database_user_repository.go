package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DatabaseUserRepository defines data access for database users.
type DatabaseUserRepository interface {
	FindByID(ctx context.Context, id string) (*models.DatabaseUser, error)
	List(ctx context.Context, opts ListOptions) ([]models.DatabaseUser, int64, error)
	ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.DatabaseUser, int64, error)
	CountByUserID(ctx context.Context, userID string) (int64, error)
	Create(ctx context.Context, du *models.DatabaseUser) error
	Delete(ctx context.Context, id string) error
	UpdatePasswordHash(ctx context.Context, id string, hash string) error
	// UpdateUsername renames the DB-user row in place (GH #1238 DB re-prefix).
	UpdateUsername(ctx context.Context, id, username string) error
	ExistsByUserAndUsername(ctx context.Context, userID string, username string) (bool, error)
}

type databaseUserRepo struct{ db *gorm.DB }

func NewDatabaseUserRepository(db *gorm.DB) DatabaseUserRepository {
	return &databaseUserRepo{db: db}
}

func (r *databaseUserRepo) FindByID(ctx context.Context, id string) (*models.DatabaseUser, error) {
	var du models.DatabaseUser
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&du).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &du, nil
}

// databaseUserListCols — only the database username is free-text searchable.
var databaseUserListCols = ListCols{
	Search:      []string{"username"},
	Sort:        []string{"username", "engine", "created_at"},
	DefaultSort: "created_at",
}

func (r *databaseUserRepo) List(ctx context.Context, opts ListOptions) ([]models.DatabaseUser, int64, error) {
	var (
		users []models.DatabaseUser
		total int64
	)
	base := r.db.WithContext(ctx).Model(&models.DatabaseUser{})

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, databaseUserListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, databaseUserListCols)
	if err := q.Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func (r *databaseUserRepo) ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.DatabaseUser, int64, error) {
	var (
		users []models.DatabaseUser
		total int64
	)
	base := r.db.WithContext(ctx).Model(&models.DatabaseUser{}).Where("user_id = ?", userID)

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, databaseUserListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, databaseUserListCols)
	if err := q.Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func (r *databaseUserRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.DatabaseUser{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *databaseUserRepo) Create(ctx context.Context, du *models.DatabaseUser) error {
	return r.db.WithContext(ctx).Create(du).Error
}

func (r *databaseUserRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.DatabaseUser{}).Error
}

func (r *databaseUserRepo) UpdatePasswordHash(ctx context.Context, id string, hash string) error {
	return r.db.WithContext(ctx).Model(&models.DatabaseUser{}).Where("id = ?", id).Update("password_hash", hash).Error
}

func (r *databaseUserRepo) UpdateUsername(ctx context.Context, id, username string) error {
	return r.db.WithContext(ctx).Model(&models.DatabaseUser{}).Where("id = ?", id).Update("username", username).Error
}

func (r *databaseUserRepo) ExistsByUserAndUsername(ctx context.Context, userID string, username string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.DatabaseUser{}).Where("user_id = ? AND username = ?", userID, username).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
