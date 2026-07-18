package repository

import (
	"context"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// AccountSkeletonRepository wraps the account_skeleton_files table — the
// server-wide set of starter files copied into a new domain's docroot on
// creation (GH #465). Rows are addressed by RelPath.
type AccountSkeletonRepository interface {
	// List returns every skeleton file WITH content, ordered by path. Used by
	// the domain.create dispatch to serialize the tree to the agent.
	List(ctx context.Context) ([]models.AccountSkeletonFile, error)
	// ListPaths returns just the relative paths (+ sizes), for the admin
	// listing UI — avoids shipping every file body on a list call.
	ListPaths(ctx context.Context) ([]SkeletonEntry, error)
	Get(ctx context.Context, relPath string) (*models.AccountSkeletonFile, error)
	// Upsert creates or replaces the file at relPath.
	Upsert(ctx context.Context, relPath string, content []byte) error
	Delete(ctx context.Context, relPath string) error
	// TotalBytes / Count back the API cap checks.
	TotalBytes(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// SkeletonEntry is a content-free listing row (path + byte size).
type SkeletonEntry struct {
	RelPath string `json:"rel_path"`
	Size    int64  `json:"size"`
}

type accountSkeletonRepo struct{ db *gorm.DB }

func NewAccountSkeletonRepository(db *gorm.DB) AccountSkeletonRepository {
	return &accountSkeletonRepo{db: db}
}

func (r *accountSkeletonRepo) List(ctx context.Context) ([]models.AccountSkeletonFile, error) {
	rows := make([]models.AccountSkeletonFile, 0)
	if err := r.db.WithContext(ctx).Order("rel_path ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *accountSkeletonRepo) ListPaths(ctx context.Context) ([]SkeletonEntry, error) {
	out := make([]SkeletonEntry, 0)
	err := r.db.WithContext(ctx).
		Model(&models.AccountSkeletonFile{}).
		Select("rel_path, OCTET_LENGTH(content) AS size").
		Order("rel_path ASC").
		Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *accountSkeletonRepo) Get(ctx context.Context, relPath string) (*models.AccountSkeletonFile, error) {
	var row models.AccountSkeletonFile
	if err := r.db.WithContext(ctx).Where("rel_path = ?", relPath).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (r *accountSkeletonRepo) Upsert(ctx context.Context, relPath string, content []byte) error {
	var existing models.AccountSkeletonFile
	err := r.db.WithContext(ctx).Where("rel_path = ?", relPath).First(&existing).Error
	if err == nil {
		return r.db.WithContext(ctx).
			Model(&existing).
			Update("content", content).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	return r.db.WithContext(ctx).Create(&models.AccountSkeletonFile{
		ID:      ids.NewULID(),
		RelPath: relPath,
		Content: content,
	}).Error
}

func (r *accountSkeletonRepo) Delete(ctx context.Context, relPath string) error {
	res := r.db.WithContext(ctx).Where("rel_path = ?", relPath).Delete(&models.AccountSkeletonFile{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *accountSkeletonRepo) TotalBytes(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).
		Model(&models.AccountSkeletonFile{}).
		Select("COALESCE(SUM(OCTET_LENGTH(content)), 0)").
		Scan(&total).Error
	return total, err
}

func (r *accountSkeletonRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.AccountSkeletonFile{}).Count(&n).Error
	return n, err
}
