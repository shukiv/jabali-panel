package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ErrGitDeploymentActive: the domain already has a deployment or rollback in
// progress.
var ErrGitDeploymentActive = errors.New("repository: a Git deployment is already in progress for this domain")

// DomainGitDeploymentRepository persists Git deployments (GH #2277,
// ADR-0178).
type DomainGitDeploymentRepository interface {
	// CreateUnlessRunning inserts run unless its domain already has a run in
	// progress (ErrGitDeploymentActive). The domain's row is locked
	// meanwhile, so two requests can't both start one.
	CreateUnlessRunning(ctx context.Context, run *models.DomainGitDeployment) error
	Save(ctx context.Context, run *models.DomainGitDeployment) error
	FindByID(ctx context.Context, id string) (*models.DomainGitDeployment, error)
	// ListByDomain returns the runs userID started on a domain, newest
	// first, without their logs: a domain's new owner doesn't see the
	// previous owner's.
	ListByDomain(ctx context.Context, domainID, userID string, offset, limit int) ([]models.DomainGitDeployment, int64, error)
	// ListRunning returns runs still in progress, oldest first.
	ListRunning(ctx context.Context, limit int) ([]models.DomainGitDeployment, error)
	// SetDocRoot points the domain's document root at docRoot, only while
	// userID still owns the domain. It reports whether the row changed:
	// false when the domain has another owner now, is gone, or already had
	// that document root.
	SetDocRoot(ctx context.Context, domainID, userID, docRoot string) (bool, error)
}

type domainGitDeploymentRepo struct{ db *gorm.DB }

func NewDomainGitDeploymentRepository(db *gorm.DB) DomainGitDeploymentRepository {
	return &domainGitDeploymentRepo{db: db}
}

func (r *domainGitDeploymentRepo) CreateUnlessRunning(ctx context.Context, run *models.DomainGitDeployment) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var dom struct{ ID string }
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Table("domains").Select("id").Where("id = ?", run.DomainID).
			Take(&dom).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var n int64
		if err := tx.Model(&models.DomainGitDeployment{}).
			Where("domain_id = ? AND state = ?", run.DomainID, models.GitDeployRunning).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrGitDeploymentActive
		}
		return tx.Create(run).Error
	})
}

func (r *domainGitDeploymentRepo) Save(ctx context.Context, run *models.DomainGitDeployment) error {
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *domainGitDeploymentRepo) FindByID(ctx context.Context, id string) (*models.DomainGitDeployment, error) {
	var run models.DomainGitDeployment
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *domainGitDeploymentRepo) ListByDomain(ctx context.Context, domainID, userID string, offset, limit int) ([]models.DomainGitDeployment, int64, error) {
	// Session: the count and the page each start from the same conditions.
	q := r.db.WithContext(ctx).Model(&models.DomainGitDeployment{}).
		Where("domain_id = ? AND user_id = ?", domainID, userID).Session(&gorm.Session{})
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	runs := []models.DomainGitDeployment{}
	err := q.Omit("log").Order("created_at DESC").Offset(offset).Limit(limit).Find(&runs).Error
	return runs, total, err
}

func (r *domainGitDeploymentRepo) ListRunning(ctx context.Context, limit int) ([]models.DomainGitDeployment, error) {
	runs := []models.DomainGitDeployment{}
	err := r.db.WithContext(ctx).Omit("log").
		Where("state = ?", models.GitDeployRunning).
		Order("created_at ASC").Limit(limit).Find(&runs).Error
	return runs, err
}

func (r *domainGitDeploymentRepo) SetDocRoot(ctx context.Context, domainID, userID, docRoot string) (bool, error) {
	// One statement: the owner check and the write can't be split by an
	// owner change, and no other column of the domain is written.
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ? AND user_id = ? AND doc_root <> ?", domainID, userID, docRoot).
		Updates(map[string]any{"doc_root": docRoot, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return false, translate(res.Error)
	}
	return res.RowsAffected == 1, nil
}
