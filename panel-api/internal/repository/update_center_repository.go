package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// M53 Updates Center repositories. Two singletons (id=1) + an append-only
// history log. See ADR-0118.

// --- update_state (singleton) -------------------------------------------

// UpdateStateRepository owns the singleton update_state row: the last-check
// snapshot the Updates page reads on load.
type UpdateStateRepository interface {
	Get(ctx context.Context) (*models.UpdateState, error)
	EnsureDefault(ctx context.Context) (*models.UpdateState, error)
	UpsertJabali(ctx context.Context, behind int, sha string, at time.Time) error
	UpsertApt(ctx context.Context, total, security int, at time.Time) error
	// UpsertAptStatus refreshes the JAB-353 OS-patch status fields (last applied
	// time, reboot-required) independently of the upgradable-package counts.
	UpsertAptStatus(ctx context.Context, lastApplied *time.Time, rebootRequired bool) error
}

type updateStateRepo struct{ db *gorm.DB }

// NewUpdateStateRepository returns a repository backed by db.
func NewUpdateStateRepository(db *gorm.DB) UpdateStateRepository {
	return &updateStateRepo{db: db}
}

func (r *updateStateRepo) EnsureDefault(ctx context.Context) (*models.UpdateState, error) {
	row := &models.UpdateState{ID: 1}
	if err := r.db.WithContext(ctx).
		Where(models.UpdateState{ID: 1}).
		Attrs(models.UpdateState{ID: 1}).
		FirstOrCreate(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (r *updateStateRepo) Get(ctx context.Context) (*models.UpdateState, error) {
	var s models.UpdateState
	if err := r.db.WithContext(ctx).Where("id = ?", 1).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *updateStateRepo) UpsertJabali(ctx context.Context, behind int, sha string, at time.Time) error {
	if _, err := r.EnsureDefault(ctx); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&models.UpdateState{}).Where("id = ?", 1).
		Updates(map[string]any{
			"jabali_behind":      behind,
			"jabali_current_sha": sha,
			"jabali_checked_at":  at,
		}).Error
}

func (r *updateStateRepo) UpsertApt(ctx context.Context, total, security int, at time.Time) error {
	if _, err := r.EnsureDefault(ctx); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&models.UpdateState{}).Where("id = ?", 1).
		Updates(map[string]any{
			"apt_total":      total,
			"apt_security":   security,
			"apt_checked_at": at,
		}).Error
}

func (r *updateStateRepo) UpsertAptStatus(ctx context.Context, lastApplied *time.Time, rebootRequired bool) error {
	if _, err := r.EnsureDefault(ctx); err != nil {
		return err
	}
	// Map-based update writes both keys explicitly (a nil lastApplied clears the
	// column, and a false rebootRequired is written, not skipped as a zero value).
	return r.db.WithContext(ctx).Model(&models.UpdateState{}).Where("id = ?", 1).
		Updates(map[string]any{
			"apt_last_applied_at": lastApplied,
			"apt_reboot_required": rebootRequired,
		}).Error
}

// --- update_history (append-only) ---------------------------------------

// UpdateHistoryRepository owns the update_history log.
type UpdateHistoryRepository interface {
	List(ctx context.Context, limit int) ([]models.UpdateHistory, error)
	ListRunning(ctx context.Context) ([]models.UpdateHistory, error)
	Insert(ctx context.Context, h *models.UpdateHistory) error
	MarkFinished(ctx context.Context, id, status, summary, logExcerpt string) error
}

type updateHistoryRepo struct{ db *gorm.DB }

// NewUpdateHistoryRepository returns a repository backed by db.
func NewUpdateHistoryRepository(db *gorm.DB) UpdateHistoryRepository {
	return &updateHistoryRepo{db: db}
}

func (r *updateHistoryRepo) List(ctx context.Context, limit int) ([]models.UpdateHistory, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []models.UpdateHistory
	if err := r.db.WithContext(ctx).Order("started_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *updateHistoryRepo) ListRunning(ctx context.Context) ([]models.UpdateHistory, error) {
	var rows []models.UpdateHistory
	if err := r.db.WithContext(ctx).
		Where("status = ?", models.UpdateStatusRunning).
		Order("started_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *updateHistoryRepo) Insert(ctx context.Context, h *models.UpdateHistory) error {
	if h.ID == "" {
		h.ID = ids.NewULID()
	}
	if h.StartedAt.IsZero() {
		h.StartedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(h).Error
}

func (r *updateHistoryRepo) MarkFinished(ctx context.Context, id, status, summary, logExcerpt string) error {
	now := time.Now()
	updates := map[string]any{
		"status":      status,
		"finished_at": now,
	}
	if summary != "" {
		updates["summary"] = summary
	}
	if logExcerpt != "" {
		updates["log_excerpt"] = logExcerpt
	}
	return r.db.WithContext(ctx).Model(&models.UpdateHistory{}).Where("id = ?", id).Updates(updates).Error
}

// --- update_autoupdate_config (singleton) -------------------------------

// UpdateAutoupdateConfigRepository owns the singleton auto-update desired state.
type UpdateAutoupdateConfigRepository interface {
	Get(ctx context.Context) (*models.UpdateAutoupdateConfig, error)
	EnsureDefault(ctx context.Context) (*models.UpdateAutoupdateConfig, error)
	Upsert(ctx context.Context, c *models.UpdateAutoupdateConfig) error
}

type updateAutoupdateConfigRepo struct{ db *gorm.DB }

// NewUpdateAutoupdateConfigRepository returns a repository backed by db.
func NewUpdateAutoupdateConfigRepository(db *gorm.DB) UpdateAutoupdateConfigRepository {
	return &updateAutoupdateConfigRepo{db: db}
}

func (r *updateAutoupdateConfigRepo) EnsureDefault(ctx context.Context) (*models.UpdateAutoupdateConfig, error) {
	row := &models.UpdateAutoupdateConfig{ID: 1}
	// JAB-353: fresh installs seed apt_enabled=TRUE (OS security auto-upgrades on
	// by default). AptEnabled:true is a non-zero attr, so it lands in the INSERT
	// on first create; existing rows are untouched (FirstOrCreate) and were
	// force-enabled by migration 000272.
	if err := r.db.WithContext(ctx).
		Where(models.UpdateAutoupdateConfig{ID: 1}).
		Attrs(models.UpdateAutoupdateConfig{ID: 1, AptEnabled: true, AptTime: "03:30", JabaliTime: "04:30"}).
		FirstOrCreate(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (r *updateAutoupdateConfigRepo) Get(ctx context.Context) (*models.UpdateAutoupdateConfig, error) {
	var c models.UpdateAutoupdateConfig
	if err := r.db.WithContext(ctx).Where("id = ?", 1).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (r *updateAutoupdateConfigRepo) Upsert(ctx context.Context, c *models.UpdateAutoupdateConfig) error {
	c.ID = 1
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"apt_enabled", "apt_optout_acknowledged", "apt_time", "jabali_enabled", "jabali_time"}),
	}).Create(c).Error
}
