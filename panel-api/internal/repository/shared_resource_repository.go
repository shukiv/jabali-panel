package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// SharedResourceRepository is the data access layer for M52 shared resources
// + their grants (ADR-0133). panel-API is the only writer; the reconciler
// reads these rows to converge the Stalwart host principals + shareWith ACLs.
type SharedResourceRepository interface {
	FindByID(ctx context.Context, id string) (*models.SharedResource, error)
	ListByDomainID(ctx context.Context, domainID string) ([]models.SharedResource, error)
	ListByDomainAndKind(ctx context.Context, domainID, kind string) ([]models.SharedResource, error)
	ListAll(ctx context.Context) ([]models.SharedResource, error)
	Create(ctx context.Context, r *models.SharedResource) error
	UpdateMeta(ctx context.Context, id, displayName string) error
	// UpdateProjection caches the Stalwart ids after the reconciler projects
	// the host principal + collection.
	UpdateProjection(ctx context.Context, id, hostAccountID, collectionID string) error
	Delete(ctx context.Context, id string) error
	ExistsByEmail(ctx context.Context, email string) (bool, error)

	// Grants.
	ListGrants(ctx context.Context, resourceID string) ([]models.SharedResourceGrant, error)
	ListAllGrants(ctx context.Context) ([]models.SharedResourceGrant, error)
	// ReplaceGrants replaces the whole grant set for a resource in one tx
	// (idempotent whole-set write, mirroring the agent's shareWith replace).
	ReplaceGrants(ctx context.Context, resourceID string, grants []models.SharedResourceGrant) error
	// PruneGranteeGrants removes every grant pointing at a deleted grantee
	// (called on mailbox/group delete — the grantee has no FK).
	PruneGranteeGrants(ctx context.Context, granteeKind, granteeID string) error

	// Tombstones — durable host teardown after a resource is deleted.
	AddTombstone(ctx context.Context, email string) error
	ListTombstones(ctx context.Context) ([]string, error)
	DeleteTombstone(ctx context.Context, email string) error
}

type sharedResourceRepo struct{ db *gorm.DB }

func NewSharedResourceRepository(db *gorm.DB) SharedResourceRepository {
	return &sharedResourceRepo{db: db}
}

func (r *sharedResourceRepo) FindByID(ctx context.Context, id string) (*models.SharedResource, error) {
	var sr models.SharedResource
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&sr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sr, nil
}

func (r *sharedResourceRepo) ListByDomainID(ctx context.Context, domainID string) ([]models.SharedResource, error) {
	var rows []models.SharedResource
	err := r.db.WithContext(ctx).Where("domain_id = ?", domainID).
		Order("kind ASC, display_name ASC").Find(&rows).Error
	return rows, err
}

func (r *sharedResourceRepo) ListByDomainAndKind(ctx context.Context, domainID, kind string) ([]models.SharedResource, error) {
	var rows []models.SharedResource
	err := r.db.WithContext(ctx).Where("domain_id = ? AND kind = ?", domainID, kind).
		Order("display_name ASC").Find(&rows).Error
	return rows, err
}

func (r *sharedResourceRepo) ListAll(ctx context.Context) ([]models.SharedResource, error) {
	var rows []models.SharedResource
	err := r.db.WithContext(ctx).Order("id ASC").Find(&rows).Error
	return rows, err
}

func (r *sharedResourceRepo) Create(ctx context.Context, sr *models.SharedResource) error {
	now := time.Now().UTC()
	if sr.CreatedAt.IsZero() {
		sr.CreatedAt = now
	}
	sr.UpdatedAt = now
	return mapPostmasterReserved(r.db.WithContext(ctx).Create(sr).Error)
}

func (r *sharedResourceRepo) UpdateMeta(ctx context.Context, id, displayName string) error {
	res := r.db.WithContext(ctx).Model(&models.SharedResource{}).
		Where("id = ?", id).
		Updates(map[string]any{"display_name": displayName, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *sharedResourceRepo) UpdateProjection(ctx context.Context, id, hostAccountID, collectionID string) error {
	res := r.db.WithContext(ctx).Model(&models.SharedResource{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"host_account_id": hostAccountID,
			"collection_id":   collectionID,
			"updated_at":      time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *sharedResourceRepo) Delete(ctx context.Context, id string) error {
	// shared_resource_grants cascade via FK.
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.SharedResource{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *sharedResourceRepo) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.SharedResource{}).
		Where("email_cached = ?", email).Count(&count).Error
	return count > 0, err
}

func (r *sharedResourceRepo) ListGrants(ctx context.Context, resourceID string) ([]models.SharedResourceGrant, error) {
	var rows []models.SharedResourceGrant
	err := r.db.WithContext(ctx).Where("resource_id = ?", resourceID).Find(&rows).Error
	return rows, err
}

func (r *sharedResourceRepo) ListAllGrants(ctx context.Context) ([]models.SharedResourceGrant, error) {
	var rows []models.SharedResourceGrant
	err := r.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

func (r *sharedResourceRepo) ReplaceGrants(ctx context.Context, resourceID string, grants []models.SharedResourceGrant) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("resource_id = ?", resourceID).Delete(&models.SharedResourceGrant{}).Error; err != nil {
			return err
		}
		if len(grants) == 0 {
			return nil
		}
		for i := range grants {
			grants[i].ResourceID = resourceID
			if grants[i].CreatedAt.IsZero() {
				grants[i].CreatedAt = now
			}
		}
		return tx.Create(&grants).Error
	})
}

func (r *sharedResourceRepo) PruneGranteeGrants(ctx context.Context, granteeKind, granteeID string) error {
	return r.db.WithContext(ctx).
		Where("grantee_kind = ? AND grantee_id = ?", granteeKind, granteeID).
		Delete(&models.SharedResourceGrant{}).Error
}

func (r *sharedResourceRepo) AddTombstone(ctx context.Context, email string) error {
	t := &models.SharedResourceTombstone{Email: email, CreatedAt: time.Now().UTC()}
	// Idempotent: ignore a duplicate tombstone for the same address.
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(t).Error
}

func (r *sharedResourceRepo) ListTombstones(ctx context.Context) ([]string, error) {
	var emails []string
	err := r.db.WithContext(ctx).Model(&models.SharedResourceTombstone{}).
		Pluck("email", &emails).Error
	return emails, err
}

func (r *sharedResourceRepo) DeleteTombstone(ctx context.Context, email string) error {
	return r.db.WithContext(ctx).
		Where("email = ?", email).
		Delete(&models.SharedResourceTombstone{}).Error
}
