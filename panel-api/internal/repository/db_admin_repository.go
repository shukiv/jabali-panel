package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DBAdminRepository backs M46 database server admin ops (ADR-0097..0100):
// the privileged-action audit trail, the curated tuning KV (reconciler
// source of truth), and long-running maintenance job state.
type DBAdminRepository interface {
	// Audit appends one privileged-action row. ID + TS are filled if
	// unset. detail must never carry a secret (caller's contract).
	Audit(ctx context.Context, a models.DBAdminAudit) error

	// --- tuning (ADR-0098) ---
	ListTuning(ctx context.Context, engine string) ([]models.DBTuningSetting, error)
	ListAllTuning(ctx context.Context) ([]models.DBTuningSetting, error)
	// UpsertTuning sets value for (engine,param), clearing applied_at
	// so the reconciler/agent re-applies. Returns the row id.
	UpsertTuning(ctx context.Context, engine, param, value string) error
	// MarkTuningApplied stamps applied_at/applied_by on every row for
	// the engine after a successful agent apply.
	MarkTuningApplied(ctx context.Context, engine, byUserID string, at time.Time) error

	// --- jobs (ADR-0100) ---
	// CreateJob inserts a job. A running job claims its engine (a unique
	// key), so a second running job for the same engine is refused with
	// ErrConflict even when two callers passed RunningJob at once.
	CreateJob(ctx context.Context, j *models.DBAdminJob) error
	// FinishJob records the outcome and releases the engine.
	FinishJob(ctx context.Context, id, status, summary string) error
	GetJob(ctx context.Context, id string) (*models.DBAdminJob, error)
	// RunningJob returns the in-flight job for an engine, or
	// ErrNotFound if none — used for the 409 concurrency guard.
	RunningJob(ctx context.Context, engine string) (*models.DBAdminJob, error)
}

type dbAdminRepo struct{ db *gorm.DB }

func NewDBAdminRepository(db *gorm.DB) DBAdminRepository { return &dbAdminRepo{db: db} }

func (r *dbAdminRepo) Audit(ctx context.Context, a models.DBAdminAudit) error {
	if a.ID == "" {
		a.ID = ids.NewULID()
	}
	if a.TS.IsZero() {
		a.TS = time.Now().UTC()
	}
	if err := r.db.WithContext(ctx).Create(&a).Error; err != nil {
		return fmt.Errorf("db admin audit insert: %w", err)
	}
	return nil
}

func (r *dbAdminRepo) ListTuning(ctx context.Context, engine string) ([]models.DBTuningSetting, error) {
	var rows []models.DBTuningSetting
	if err := r.db.WithContext(ctx).Where("engine = ?", engine).
		Order("param ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tuning: %w", err)
	}
	return rows, nil
}

func (r *dbAdminRepo) ListAllTuning(ctx context.Context) ([]models.DBTuningSetting, error) {
	var rows []models.DBTuningSetting
	if err := r.db.WithContext(ctx).Order("engine ASC, param ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list all tuning: %w", err)
	}
	return rows, nil
}

func (r *dbAdminRepo) UpsertTuning(ctx context.Context, engine, param, value string) error {
	now := time.Now().UTC()
	row := models.DBTuningSetting{
		ID:        ids.NewULID(),
		Engine:    engine,
		Param:     param,
		Value:     value,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// On (engine,param) conflict update value + clear applied_at so
	// the reconciler re-converges (ADR-0098).
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "engine"}, {Name: "param"}},
		DoUpdates: clause.Assignments(map[string]any{"value": value, "applied_at": nil, "updated_at": now}),
	}).Create(&row).Error
	if err != nil {
		return fmt.Errorf("upsert tuning: %w", err)
	}
	return nil
}

func (r *dbAdminRepo) MarkTuningApplied(ctx context.Context, engine, byUserID string, at time.Time) error {
	err := r.db.WithContext(ctx).Model(&models.DBTuningSetting{}).
		Where("engine = ?", engine).
		Updates(map[string]any{"applied_at": at, "applied_by": byUserID}).Error
	if err != nil {
		return fmt.Errorf("mark tuning applied: %w", err)
	}
	return nil
}

func (r *dbAdminRepo) CreateJob(ctx context.Context, j *models.DBAdminJob) error {
	if j.ID == "" {
		j.ID = ids.NewULID()
	}
	if j.StartedAt.IsZero() {
		j.StartedAt = time.Now().UTC()
	}
	if j.Status == "" {
		j.Status = "running"
	}
	j.RunningEngine = nil
	if j.Status == "running" {
		engine := j.Engine
		j.RunningEngine = &engine
	}
	if err := r.db.WithContext(ctx).Create(j).Error; err != nil {
		if errors.Is(translate(err), ErrConflict) {
			return ErrConflict
		}
		return fmt.Errorf("create db admin job: %w", err)
	}
	return nil
}

func (r *dbAdminRepo) FinishJob(ctx context.Context, id, status, summary string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&models.DBAdminJob{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "summary": summary, "finished_at": now, "running_engine": nil}).Error
	if err != nil {
		return fmt.Errorf("finish db admin job: %w", err)
	}
	return nil
}

func (r *dbAdminRepo) GetJob(ctx context.Context, id string) (*models.DBAdminJob, error) {
	var j models.DBAdminJob
	if err := r.db.WithContext(ctx).First(&j, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get db admin job: %w", err)
	}
	return &j, nil
}

func (r *dbAdminRepo) RunningJob(ctx context.Context, engine string) (*models.DBAdminJob, error) {
	var j models.DBAdminJob
	err := r.db.WithContext(ctx).
		Where("engine = ? AND status = ?", engine, "running").
		Order("started_at DESC").First(&j).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("running job lookup: %w", err)
	}
	return &j, nil
}
