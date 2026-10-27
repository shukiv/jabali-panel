package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ErrComposerRunActive: the domain, or the folder the run is for, already
// has a run in progress.
var ErrComposerRunActive = errors.New("repository: a Composer run is already in progress for this domain or its project folder")

// ComposerRunRepository persists Composer runs (GH #2276, ADR-0176).
type ComposerRunRepository interface {
	// CreateUnlessRunning inserts run unless its domain, or another of the
	// user's domains in the same folder (WorkDir), already has a run in
	// progress (ErrComposerRunActive). The user's row is locked meanwhile:
	// domains that share a project folder share their owner, so two requests
	// can't both start a run in one folder.
	CreateUnlessRunning(ctx context.Context, run *models.ComposerRun) error
	Save(ctx context.Context, run *models.ComposerRun) error
	FindByID(ctx context.Context, id string) (*models.ComposerRun, error)
	// ListByDomain returns a domain's runs, newest first, without their logs.
	ListByDomain(ctx context.Context, domainID string, offset, limit int) ([]models.ComposerRun, int64, error)
	// ListRunning returns runs still in progress, oldest first.
	ListRunning(ctx context.Context, limit int) ([]models.ComposerRun, error)
}

type composerRunRepo struct{ db *gorm.DB }

func NewComposerRunRepository(db *gorm.DB) ComposerRunRepository {
	return &composerRunRepo{db: db}
}

func (r *composerRunRepo) CreateUnlessRunning(ctx context.Context, run *models.ComposerRun) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct{ ID string }
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Table("users").Select("id").Where("id = ?", run.UserID).
			Take(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var dom struct{ ID string }
		if err := tx.Table("domains").Select("id").
			Where("id = ? AND user_id = ?", run.DomainID, run.UserID).
			Take(&dom).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var n int64
		if err := tx.Model(&models.ComposerRun{}).
			Where("user_id = ? AND state = ? AND (domain_id = ? OR work_dir = ?)",
				run.UserID, models.ComposerRunRunning, run.DomainID, run.WorkDir).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrComposerRunActive
		}
		return tx.Create(run).Error
	})
}

func (r *composerRunRepo) Save(ctx context.Context, run *models.ComposerRun) error {
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *composerRunRepo) FindByID(ctx context.Context, id string) (*models.ComposerRun, error) {
	var run models.ComposerRun
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *composerRunRepo) ListByDomain(ctx context.Context, domainID string, offset, limit int) ([]models.ComposerRun, int64, error) {
	// Session: the count and the page each start from the same conditions.
	q := r.db.WithContext(ctx).Model(&models.ComposerRun{}).Where("domain_id = ?", domainID).Session(&gorm.Session{})
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	runs := []models.ComposerRun{}
	err := q.Omit("log").Order("created_at DESC").Offset(offset).Limit(limit).Find(&runs).Error
	return runs, total, err
}

func (r *composerRunRepo) ListRunning(ctx context.Context, limit int) ([]models.ComposerRun, error) {
	runs := []models.ComposerRun{}
	err := r.db.WithContext(ctx).Omit("log").
		Where("state = ?", models.ComposerRunRunning).
		Order("created_at ASC").Limit(limit).Find(&runs).Error
	return runs, err
}
