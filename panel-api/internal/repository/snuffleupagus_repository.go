// Package repository — Snuffleupagus state + overrides + incidents.
// M41, ADR-0088.
package repository

import (
	"context"
	"database/sql"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SnuffleupagusRepository interface {
	GetState(ctx context.Context) (*models.SnuffleupagusState, error)
	UpdateState(ctx context.Context, mode models.SnuffleupagusMode, appliedAt *time.Time, sha256 *string) error
	ListOverrides(ctx context.Context) ([]models.SnuffleupagusRuleOverride, error)
	UpsertOverride(ctx context.Context, ov *models.SnuffleupagusRuleOverride) error
	InsertIncident(ctx context.Context, in *models.SnuffleupagusIncident) error
	ListIncidents(ctx context.Context, opts IncidentListOptions) ([]models.SnuffleupagusIncident, int64, error)
	// LatestIncidentTs is the newest stored incident's time, nil when there
	// is none. The ingest resumes after it on panel start (GH #2262).
	LatestIncidentTs(ctx context.Context) (*time.Time, error)
	// DeleteAllIncidents clears the incident history (GH #2262) and returns
	// how many rows it deleted.
	DeleteAllIncidents(ctx context.Context) (int64, error)
}

type IncidentListOptions struct {
	Since    *time.Time
	Rule     string
	DomainID string
	Limit    int
	Offset   int
}

type snuffleupagusRepo struct{ db *gorm.DB }

func NewSnuffleupagusRepository(db *gorm.DB) SnuffleupagusRepository {
	return &snuffleupagusRepo{db: db}
}

func (r *snuffleupagusRepo) GetState(ctx context.Context) (*models.SnuffleupagusState, error) {
	var s models.SnuffleupagusState
	if err := r.db.WithContext(ctx).First(&s, 1).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *snuffleupagusRepo) UpdateState(ctx context.Context, mode models.SnuffleupagusMode, appliedAt *time.Time, sha256 *string) error {
	updates := map[string]any{"mode": mode}
	if appliedAt != nil {
		updates["last_applied_at"] = *appliedAt
	}
	if sha256 != nil {
		updates["last_applied_sha256"] = *sha256
	}
	return r.db.WithContext(ctx).
		Model(&models.SnuffleupagusState{}).
		Where("id = ?", 1).
		Updates(updates).Error
}

func (r *snuffleupagusRepo) ListOverrides(ctx context.Context) ([]models.SnuffleupagusRuleOverride, error) {
	var out []models.SnuffleupagusRuleOverride
	if err := r.db.WithContext(ctx).Order("rule_name").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *snuffleupagusRepo) UpsertOverride(ctx context.Context, ov *models.SnuffleupagusRuleOverride) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "rule_name"}},
			DoUpdates: clause.AssignmentColumns([]string{"enabled", "reason", "set_by_user_id", "set_at"}),
		}).
		Create(ov).Error
}

func (r *snuffleupagusRepo) InsertIncident(ctx context.Context, in *models.SnuffleupagusIncident) error {
	return r.db.WithContext(ctx).Create(in).Error
}

func (r *snuffleupagusRepo) ListIncidents(ctx context.Context, opts IncidentListOptions) ([]models.SnuffleupagusIncident, int64, error) {
	q := r.db.WithContext(ctx).Model(&models.SnuffleupagusIncident{})
	if opts.Since != nil {
		q = q.Where("ts >= ?", *opts.Since)
	}
	if opts.Rule != "" {
		q = q.Where("rule_name = ?", opts.Rule)
	}
	if opts.DomainID != "" {
		q = q.Where("domain_id = ?", opts.DomainID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	var out []models.SnuffleupagusIncident
	if err := q.Order("ts DESC").Limit(opts.Limit).Offset(opts.Offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *snuffleupagusRepo) LatestIncidentTs(ctx context.Context) (*time.Time, error) {
	var latest sql.NullTime
	if err := r.db.WithContext(ctx).Model(&models.SnuffleupagusIncident{}).
		Select("MAX(ts)").Scan(&latest).Error; err != nil {
		return nil, err
	}
	if !latest.Valid {
		return nil, nil
	}
	t := latest.Time.UTC()
	return &t, nil
}

func (r *snuffleupagusRepo) DeleteAllIncidents(ctx context.Context) (int64, error) {
	res := r.db.WithContext(ctx).Exec("DELETE FROM snuffleupagus_incidents")
	return res.RowsAffected, res.Error
}
