package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// SharedCertificateRepository is the data access for JAB-170 shared
// wildcard/multi-SAN certificates (uploaded once, served from many domains).
type SharedCertificateRepository interface {
	Create(ctx context.Context, c *models.SharedCertificate) error
	FindByID(ctx context.Context, id string) (*models.SharedCertificate, error)
	// ListAll returns every shared cert (admin surface), newest first.
	ListAll(ctx context.Context) ([]models.SharedCertificate, error)
	// ListByUserID returns a tenant's own shared certs, newest first.
	ListByUserID(ctx context.Context, userID string) ([]models.SharedCertificate, error)
	// ListServerWideAndOwned returns candidates a domain owned by ownerID may
	// auto-attach: server-wide certs (user_id NULL) plus the owner's own.
	ListServerWideAndOwned(ctx context.Context, ownerID string) ([]models.SharedCertificate, error)
	// UpdateInstalled records the on-disk paths + parsed SANs + expiry after a
	// (re)upload landed the pair on disk.
	UpdateInstalled(ctx context.Context, id, certPath, keyPath, sansJSON string, expiresAt time.Time) error
	Delete(ctx context.Context, id string) error
	// CountAttachedDomains reports how many domains currently point at this cert
	// (so delete can refuse to strand attached domains).
	CountAttachedDomains(ctx context.Context, id string) (int64, error)
}

type sharedCertificateRepo struct{ db *gorm.DB }

func NewSharedCertificateRepository(db *gorm.DB) SharedCertificateRepository {
	return &sharedCertificateRepo{db: db}
}

func (r *sharedCertificateRepo) Create(ctx context.Context, c *models.SharedCertificate) error {
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *sharedCertificateRepo) FindByID(ctx context.Context, id string) (*models.SharedCertificate, error) {
	var c models.SharedCertificate
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (r *sharedCertificateRepo) ListAll(ctx context.Context) ([]models.SharedCertificate, error) {
	var cs []models.SharedCertificate
	err := r.db.WithContext(ctx).Order("created_at DESC").Find(&cs).Error
	return cs, err
}

func (r *sharedCertificateRepo) ListByUserID(ctx context.Context, userID string) ([]models.SharedCertificate, error) {
	var cs []models.SharedCertificate
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&cs).Error
	return cs, err
}

func (r *sharedCertificateRepo) ListServerWideAndOwned(ctx context.Context, ownerID string) ([]models.SharedCertificate, error) {
	var cs []models.SharedCertificate
	err := r.db.WithContext(ctx).Where("user_id IS NULL OR user_id = ?", ownerID).Find(&cs).Error
	return cs, err
}

func (r *sharedCertificateRepo) UpdateInstalled(ctx context.Context, id, certPath, keyPath, sansJSON string, expiresAt time.Time) error {
	return r.db.WithContext(ctx).Model(&models.SharedCertificate{}).Where("id = ?", id).Updates(map[string]any{
		"cert_path":  certPath,
		"key_path":   keyPath,
		"sans":       sansJSON,
		"expires_at": expiresAt,
		"updated_at": time.Now().UTC(),
	}).Error
}

func (r *sharedCertificateRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.SharedCertificate{}).Error
}

func (r *sharedCertificateRepo) CountAttachedDomains(ctx context.Context, id string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.Domain{}).Where("shared_certificate_id = ?", id).Count(&n).Error
	return n, err
}
