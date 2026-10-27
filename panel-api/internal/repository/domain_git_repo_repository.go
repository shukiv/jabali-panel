package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainGitRepoRepository is data access for the Git repository a domain
// deploys from (GH #2277). The token is stored sealed; this layer never sees
// the plaintext.
type DomainGitRepoRepository interface {
	// Get returns the domain's repository; ErrNotFound when it has none.
	Get(ctx context.Context, domainID string) (*models.DomainGitRepo, error)
	// Put stores the domain's repository, replacing every field of an
	// earlier one, the token included.
	Put(ctx context.Context, row *models.DomainGitRepo) error
	// Delete removes the domain's repository; ErrNotFound when it has none.
	Delete(ctx context.Context, domainID string) error
}

type domainGitRepoRepo struct {
	db *gorm.DB
}

func NewDomainGitRepoRepository(db *gorm.DB) DomainGitRepoRepository {
	return &domainGitRepoRepo{db: db}
}

func (r *domainGitRepoRepo) Get(ctx context.Context, domainID string) (*models.DomainGitRepo, error) {
	var row models.DomainGitRepo
	if err := r.db.WithContext(ctx).Where("domain_id = ?", domainID).First(&row).Error; err != nil {
		return nil, translate(err)
	}
	return &row, nil
}

func (r *domainGitRepoRepo) Put(ctx context.Context, row *models.DomainGitRepo) error {
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	row.UpdatedAt = now
	return translate(r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "domain_id"}},
		// token_enc is assigned even when nil, so clearing the token clears
		// the column.
		DoUpdates: clause.AssignmentColumns([]string{
			"user_id", "repo_url", "branch", "username", "token_enc",
			"web_dir", "shared_paths", "deploy_commands", "verified_at", "updated_at",
		}),
	}).Create(row).Error)
}

func (r *domainGitRepoRepo) Delete(ctx context.Context, domainID string) error {
	res := r.db.WithContext(ctx).Where("domain_id = ?", domainID).Delete(&models.DomainGitRepo{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
