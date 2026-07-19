// Package repository — PythonApp repository (ADR-0131 / GH #203).
//
// REST handlers + the reconciler call into this; the agent never touches
// the DB. python_app_env rows CASCADE on app delete (handled here, since
// the schema has no FK CASCADE — Delete removes env then the app row).
package repository

import (
	"context"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// PythonAppRepository is the data-access surface for python_apps +
// python_app_env.
type PythonAppRepository interface {
	Create(ctx context.Context, app *models.PythonApp) error
	FindByID(ctx context.Context, id string) (*models.PythonApp, error)
	ListAll(ctx context.Context) ([]*models.PythonApp, error)
	ListByUser(ctx context.Context, userID string) ([]*models.PythonApp, error)
	ListByDomain(ctx context.Context, domainID string) ([]*models.PythonApp, error)
	Update(ctx context.Context, app *models.PythonApp) error
	UpdateStatus(ctx context.Context, id, status string, lastError *string) error
	// MarkScaffolded stamps scaffolded_at=now so the reconciler's one-shot
	// framework scaffold never re-runs over the tenant's code.
	MarkScaffolded(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error

	// env
	ListEnv(ctx context.Context, appID string) ([]*models.PythonAppEnv, error)
	ReplaceEnv(ctx context.Context, appID string, env []models.PythonAppEnv) error

	// FindFreeLoopbackPort returns the lowest free port in [20000,29999] not
	// bound by another python_app. ErrNotFound if the pool is exhausted.
	FindFreeLoopbackPort(ctx context.Context) (int, error)
}

type pythonAppRepo struct{ db *gorm.DB }

// NewPythonAppRepository constructs the gorm-backed repository.
func NewPythonAppRepository(db *gorm.DB) PythonAppRepository {
	return &pythonAppRepo{db: db}
}

func (r *pythonAppRepo) Create(ctx context.Context, app *models.PythonApp) error {
	return translate(r.db.WithContext(ctx).Create(app).Error)
}

func (r *pythonAppRepo) FindByID(ctx context.Context, id string) (*models.PythonApp, error) {
	var a models.PythonApp
	if err := r.db.WithContext(ctx).First(&a, "id = ?", id).Error; err != nil {
		return nil, translate(err)
	}
	return &a, nil
}

func (r *pythonAppRepo) ListAll(ctx context.Context) ([]*models.PythonApp, error) {
	var apps []*models.PythonApp
	if err := r.db.WithContext(ctx).Order("created_at").Find(&apps).Error; err != nil {
		return nil, translate(err)
	}
	return apps, nil
}

func (r *pythonAppRepo) ListByUser(ctx context.Context, userID string) ([]*models.PythonApp, error) {
	var apps []*models.PythonApp
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at").Find(&apps).Error; err != nil {
		return nil, translate(err)
	}
	return apps, nil
}

func (r *pythonAppRepo) ListByDomain(ctx context.Context, domainID string) ([]*models.PythonApp, error) {
	var apps []*models.PythonApp
	if err := r.db.WithContext(ctx).Where("domain_id = ?", domainID).Order("created_at").Find(&apps).Error; err != nil {
		return nil, translate(err)
	}
	return apps, nil
}

func (r *pythonAppRepo) Update(ctx context.Context, app *models.PythonApp) error {
	return translate(r.db.WithContext(ctx).Save(app).Error)
}

func (r *pythonAppRepo) UpdateStatus(ctx context.Context, id, status string, lastError *string) error {
	return translate(r.db.WithContext(ctx).Model(&models.PythonApp{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "last_error": lastError}).Error)
}

func (r *pythonAppRepo) MarkScaffolded(ctx context.Context, id string) error {
	return translate(r.db.WithContext(ctx).Model(&models.PythonApp{}).
		Where("id = ?", id).
		Update("scaffolded_at", gorm.Expr("CURRENT_TIMESTAMP")).Error)
}

func (r *pythonAppRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("app_id = ?", id).Delete(&models.PythonAppEnv{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.PythonApp{}, "id = ?", id).Error
	})
}

func (r *pythonAppRepo) ListEnv(ctx context.Context, appID string) ([]*models.PythonAppEnv, error) {
	var env []*models.PythonAppEnv
	if err := r.db.WithContext(ctx).Where("app_id = ?", appID).Order("env_key").Find(&env).Error; err != nil {
		return nil, translate(err)
	}
	return env, nil
}

// ReplaceEnv swaps the app's env set atomically (delete-all + insert).
func (r *pythonAppRepo) ReplaceEnv(ctx context.Context, appID string, env []models.PythonAppEnv) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("app_id = ?", appID).Delete(&models.PythonAppEnv{}).Error; err != nil {
			return err
		}
		for i := range env {
			env[i].AppID = appID
			if err := tx.Create(&env[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *pythonAppRepo) FindFreeLoopbackPort(ctx context.Context) (int, error) {
	var used []int
	if err := r.db.WithContext(ctx).Model(&models.PythonApp{}).
		Where("loopback_port IS NOT NULL").
		Pluck("loopback_port", &used).Error; err != nil {
		return 0, translate(err)
	}
	taken := make(map[int]bool, len(used))
	for _, p := range used {
		taken[p] = true
	}
	for port := 20000; port <= 29999; port++ {
		if !taken[port] {
			return port, nil
		}
	}
	return 0, ErrNotFound
}
