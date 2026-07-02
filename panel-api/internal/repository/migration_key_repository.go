package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MigrationKeyRepository persists the GH #648 one-time migration keys. The
// security-load-bearing method is Claim: an atomic conditional UPDATE that
// exchanges an UNCLAIMED, unexpired key for an upload session in a single row
// operation, so two concurrent claims can never both succeed.
type MigrationKeyRepository interface {
	Create(ctx context.Context, k *models.MigrationKey) error
	FindByID(ctx context.Context, id string) (*models.MigrationKey, error)
	FindByJobID(ctx context.Context, jobID string) (*models.MigrationKey, error)
	// FindByKeyHash returns the key row for a claim attempt. The handler still
	// does a constant-time compare of the plaintext-derived hash before trusting
	// it; this is a lookup, not the authorization.
	FindByKeyHash(ctx context.Context, keyHash string) (*models.MigrationKey, error)
	// FindByUploadSessionHash authenticates a post-claim upload call.
	FindByUploadSessionHash(ctx context.Context, sessionHash string) (*models.MigrationKey, error)
	// Claim atomically flips unclaimed+unexpired -> claimed, pinning the source
	// URL + the upload-session hash. Returns ErrNotFound if the row was already
	// claimed/expired/revoked (the one-time guarantee). now is injected for tests.
	Claim(ctx context.Context, id, sourceURL, uploadSessionHash string, now time.Time) error
	UpdateStatus(ctx context.Context, id, status string) error
	Revoke(ctx context.Context, id string) error
	// ReapExpired revokes every non-terminal key past its expiry (reaper timer).
	ReapExpired(ctx context.Context, now time.Time) (int64, error)
}

type migrationKeyRepo struct{ db *gorm.DB }

func NewMigrationKeyRepository(db *gorm.DB) MigrationKeyRepository {
	return &migrationKeyRepo{db: db}
}

func (r *migrationKeyRepo) Create(ctx context.Context, k *models.MigrationKey) error {
	return r.db.WithContext(ctx).Create(k).Error
}

func (r *migrationKeyRepo) findOne(ctx context.Context, where string, arg any) (*models.MigrationKey, error) {
	var k models.MigrationKey
	if err := r.db.WithContext(ctx).Where(where, arg).First(&k).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &k, nil
}

func (r *migrationKeyRepo) FindByID(ctx context.Context, id string) (*models.MigrationKey, error) {
	return r.findOne(ctx, "id = ?", id)
}

func (r *migrationKeyRepo) FindByJobID(ctx context.Context, jobID string) (*models.MigrationKey, error) {
	return r.findOne(ctx, "job_id = ?", jobID)
}

func (r *migrationKeyRepo) FindByKeyHash(ctx context.Context, keyHash string) (*models.MigrationKey, error) {
	if keyHash == "" {
		return nil, ErrNotFound
	}
	return r.findOne(ctx, "key_hash = ?", keyHash)
}

func (r *migrationKeyRepo) FindByUploadSessionHash(ctx context.Context, sessionHash string) (*models.MigrationKey, error) {
	if sessionHash == "" {
		return nil, ErrNotFound
	}
	return r.findOne(ctx, "upload_session_hash = ?", sessionHash)
}

func (r *migrationKeyRepo) Claim(ctx context.Context, id, sourceURL, uploadSessionHash string, now time.Time) error {
	res := r.db.WithContext(ctx).Model(&models.MigrationKey{}).
		Where("id = ? AND status = ? AND expires_at > ?", id, models.MigrationKeyUnclaimed, now).
		Updates(map[string]any{
			"status":              models.MigrationKeyClaimed,
			"claimed_source_url":  sourceURL,
			"upload_session_hash": uploadSessionHash,
			"updated_at":          now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Already claimed, expired, or revoked — the one-time guard.
		return ErrNotFound
	}
	return nil
}

func (r *migrationKeyRepo) UpdateStatus(ctx context.Context, id, status string) error {
	return r.db.WithContext(ctx).Model(&models.MigrationKey{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()}).Error
}

func (r *migrationKeyRepo) Revoke(ctx context.Context, id string) error {
	return r.UpdateStatus(ctx, id, models.MigrationKeyRevoked)
}

func (r *migrationKeyRepo) ReapExpired(ctx context.Context, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&models.MigrationKey{}).
		Where("expires_at <= ? AND status NOT IN ?", now,
			[]string{models.MigrationKeyDone, models.MigrationKeyRevoked}).
		Updates(map[string]any{"status": models.MigrationKeyRevoked, "updated_at": now})
	return res.RowsAffected, res.Error
}
