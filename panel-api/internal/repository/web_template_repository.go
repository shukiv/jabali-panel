package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// WebTemplateRepository persists admin-managed web (nginx) templates
// (GH #1624 / ADR-0169 Phase 3). A template is a flat row — a name, a
// description, and one raw nginx-directives blob — so, unlike
// DNSTemplateRepository, there is no child record set to load.
type WebTemplateRepository interface {
	Create(ctx context.Context, tmpl *models.WebTemplate) error
	FindByID(ctx context.Context, id string) (*models.WebTemplate, error)
	List(ctx context.Context) ([]models.WebTemplate, error)
	Update(ctx context.Context, tmpl *models.WebTemplate) error
	Delete(ctx context.Context, id string) error
}

type webTemplateRepo struct{ db *gorm.DB }

func NewWebTemplateRepository(db *gorm.DB) WebTemplateRepository {
	return &webTemplateRepo{db: db}
}

func (r *webTemplateRepo) Create(ctx context.Context, tmpl *models.WebTemplate) error {
	now := time.Now().UTC()
	if tmpl.CreatedAt.IsZero() {
		tmpl.CreatedAt = now
	}
	tmpl.UpdatedAt = now
	return r.db.WithContext(ctx).Create(tmpl).Error
}

func (r *webTemplateRepo) FindByID(ctx context.Context, id string) (*models.WebTemplate, error) {
	var tmpl models.WebTemplate
	err := r.db.WithContext(ctx).First(&tmpl, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &tmpl, nil
}

func (r *webTemplateRepo) List(ctx context.Context) ([]models.WebTemplate, error) {
	var tmpls []models.WebTemplate
	if err := r.db.WithContext(ctx).Order("name asc").Find(&tmpls).Error; err != nil {
		return nil, err
	}
	return tmpls, nil
}

// Update replaces the mutable fields via a column-scoped map so a cleared
// description ('') is written (a struct update would skip the zero value).
func (r *webTemplateRepo) Update(ctx context.Context, tmpl *models.WebTemplate) error {
	tmpl.UpdatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&models.WebTemplate{}).
		Where("id = ?", tmpl.ID).
		Updates(map[string]any{
			"name":             tmpl.Name,
			"description":      tmpl.Description,
			"nginx_directives": tmpl.NginxDirectives,
			"updated_at":       tmpl.UpdatedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *webTemplateRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.WebTemplate{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
