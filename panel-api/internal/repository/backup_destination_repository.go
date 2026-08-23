// Package repository — BackupDestinationRepository owns backup_destinations.
// M30.1 / ADR-0078.
package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type BackupDestinationRepository interface {
	Create(ctx context.Context, d *models.BackupDestination) error
	Get(ctx context.Context, id string) (*models.BackupDestination, error)
	GetByName(ctx context.Context, name string) (*models.BackupDestination, error)
	List(ctx context.Context) ([]models.BackupDestination, error)
	ListEnabled(ctx context.Context) ([]models.BackupDestination, error)
	Update(ctx context.Context, d *models.BackupDestination) error
	Delete(ctx context.Context, id string) error
	// M30.2: per-destination restic password (AES-GCM via sso.key).
	SetPassword(ctx context.Context, id string, sealed []byte, rotatedAt time.Time) error

	// JAB-362 circuit-breaker. RecordFailure sets the destination's
	// consecutive-failure count + backoff-until after a 4h stall-failure.
	// ResetHealth clears them on the next successful seal. ListBackedOffIDs
	// returns the set of destination ids currently in backoff (backoff_until >
	// now), which the dispatcher skips.
	RecordFailure(ctx context.Context, id string, failures int, backoffUntil time.Time) error
	ResetHealth(ctx context.Context, id string) error
	ListBackedOffIDs(ctx context.Context, now time.Time) (map[string]bool, error)
}

type backupDestinationRepo struct{ db *gorm.DB }

func NewBackupDestinationRepository(db *gorm.DB) BackupDestinationRepository {
	return &backupDestinationRepo{db: db}
}

func (r *backupDestinationRepo) RecordFailure(ctx context.Context, id string, failures int, backoffUntil time.Time) error {
	return translate(r.db.WithContext(ctx).
		Model(&models.BackupDestination{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"consecutive_failures": failures,
			"backoff_until":        backoffUntil,
		}).Error)
}

func (r *backupDestinationRepo) ResetHealth(ctx context.Context, id string) error {
	return translate(r.db.WithContext(ctx).
		Model(&models.BackupDestination{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"consecutive_failures": 0,
			"backoff_until":        nil,
		}).Error)
}

func (r *backupDestinationRepo) ListBackedOffIDs(ctx context.Context, now time.Time) (map[string]bool, error) {
	var ids []string
	err := r.db.WithContext(ctx).
		Model(&models.BackupDestination{}).
		Where("backoff_until IS NOT NULL AND backoff_until > ?", now).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (r *backupDestinationRepo) Create(ctx context.Context, d *models.BackupDestination) error {
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = now
	}
	if err := r.db.WithContext(ctx).Create(d).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *backupDestinationRepo) Get(ctx context.Context, id string) (*models.BackupDestination, error) {
	var out models.BackupDestination
	if err := r.db.WithContext(ctx).First(&out, "id = ?", id).Error; err != nil {
		return nil, translate(err)
	}
	return &out, nil
}

func (r *backupDestinationRepo) GetByName(ctx context.Context, name string) (*models.BackupDestination, error) {
	var out models.BackupDestination
	if err := r.db.WithContext(ctx).First(&out, "name = ?", name).Error; err != nil {
		return nil, translate(err)
	}
	return &out, nil
}

func (r *backupDestinationRepo) List(ctx context.Context) ([]models.BackupDestination, error) {
	var out []models.BackupDestination
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupDestinationRepo) ListEnabled(ctx context.Context) ([]models.BackupDestination, error) {
	var out []models.BackupDestination
	if err := r.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("name ASC").
		Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupDestinationRepo) Update(ctx context.Context, d *models.BackupDestination) error {
	d.UpdatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&models.BackupDestination{}).
		Where("id = ?", d.ID).
		Updates(map[string]any{
			"name":            d.Name,
			"kind":            d.Kind,
			"url":             d.URL,
			"credentials_ref": d.CredentialsRef,
			"extra_options":   d.ExtraOptions,
			"enabled":         d.Enabled,
			"updated_at":      d.UpdatedAt,
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *backupDestinationRepo) SetPassword(ctx context.Context, id string, sealed []byte, rotatedAt time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&models.BackupDestination{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"password_enc":        sealed,
			"password_rotated_at": rotatedAt,
			"updated_at":          time.Now().UTC(),
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *backupDestinationRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.BackupDestination{}, "id = ?", id)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
