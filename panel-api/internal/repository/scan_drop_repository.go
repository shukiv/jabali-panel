package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ScanDropRepository stores the scanners that feed shared Files resources
// (GH #2503). panel-api is the only writer; the ingest loop reads every row
// each pass, records what it moved, and deletes rows whose resource or login
// is gone.
type ScanDropRepository interface {
	FindByResourceID(ctx context.Context, resourceID string) (*models.ScanDrop, error)
	ListAll(ctx context.Context) ([]models.ScanDrop, error)
	// Create inserts a row. A resource that already has a scanner, or a login
	// already used by one, is ErrConflict.
	Create(ctx context.Context, d *models.ScanDrop) error
	Delete(ctx context.Context, resourceID string) error
	// RecordIngest adds moved to ingested_total, stamps last_ingest_at when
	// anything moved, and sets last_error ("" clears it).
	RecordIngest(ctx context.Context, resourceID string, moved uint64, lastError string) error
}

type scanDropRepo struct{ db *gorm.DB }

func NewScanDropRepository(db *gorm.DB) ScanDropRepository {
	return &scanDropRepo{db: db}
}

func (r *scanDropRepo) FindByResourceID(ctx context.Context, resourceID string) (*models.ScanDrop, error) {
	var d models.ScanDrop
	if err := r.db.WithContext(ctx).Where("resource_id = ?", resourceID).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (r *scanDropRepo) ListAll(ctx context.Context) ([]models.ScanDrop, error) {
	var rows []models.ScanDrop
	err := r.db.WithContext(ctx).Order("resource_id ASC").Find(&rows).Error
	return rows, err
}

func (r *scanDropRepo) Create(ctx context.Context, d *models.ScanDrop) error {
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(d).Error; err != nil {
		if isDuplicateKey(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (r *scanDropRepo) Delete(ctx context.Context, resourceID string) error {
	res := r.db.WithContext(ctx).Where("resource_id = ?", resourceID).Delete(&models.ScanDrop{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *scanDropRepo) RecordIngest(ctx context.Context, resourceID string, moved uint64, lastError string) error {
	if len(lastError) > 512 {
		lastError = strings.ToValidUTF8(lastError[:512], "")
	}
	now := time.Now().UTC()
	updates := map[string]any{"last_error": lastError, "updated_at": now}
	if moved > 0 {
		updates["ingested_total"] = gorm.Expr("ingested_total + ?", moved)
		updates["last_ingest_at"] = now
	}
	return r.db.WithContext(ctx).Model(&models.ScanDrop{}).
		Where("resource_id = ?", resourceID).Updates(updates).Error
}
