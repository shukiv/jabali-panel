package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DNSTemplateRepository persists admin-managed custom DNS templates (GH #1627).
// A template owns an ordered set of record blueprints in a child table; the
// repository loads them explicitly (never a lazy association) so the API and
// the reconciler control the query. Create/Update write the template and its
// records atomically so a half-written template can never be seeded.
type DNSTemplateRepository interface {
	// Create inserts the template and its Records in one transaction. The
	// caller sets IDs + timestamps on the template; the repo stamps each
	// record's ID/TemplateID/SortOrder/timestamps.
	Create(ctx context.Context, tmpl *models.DNSTemplate) error
	// FindByID returns the template WITH its records ordered by sort_order.
	FindByID(ctx context.Context, id string) (*models.DNSTemplate, error)
	// List returns every template WITH its records (admin management view).
	List(ctx context.Context) ([]models.DNSTemplate, error)
	// ListSummaries returns every template WITHOUT records (the tenant picker
	// only needs id/name/description).
	ListSummaries(ctx context.Context) ([]models.DNSTemplate, error)
	// Update replaces the template's name/description AND its full record set
	// (delete-all + re-insert) in one transaction.
	Update(ctx context.Context, tmpl *models.DNSTemplate) error
	// Delete removes the template; its records go via ON DELETE CASCADE.
	Delete(ctx context.Context, id string) error
}

type dnsTemplateRepo struct{ db *gorm.DB }

func NewDNSTemplateRepository(db *gorm.DB) DNSTemplateRepository {
	return &dnsTemplateRepo{db: db}
}

func (r *dnsTemplateRepo) Create(ctx context.Context, tmpl *models.DNSTemplate) error {
	now := time.Now().UTC()
	if tmpl.CreatedAt.IsZero() {
		tmpl.CreatedAt = now
	}
	tmpl.UpdatedAt = now
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("Records").Create(tmpl).Error; err != nil {
			return err
		}
		return insertTemplateRecords(tx, tmpl.ID, tmpl.Records, now)
	})
}

func (r *dnsTemplateRepo) FindByID(ctx context.Context, id string) (*models.DNSTemplate, error) {
	var tmpl models.DNSTemplate
	err := r.db.WithContext(ctx).First(&tmpl, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	recs, err := r.recordsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	tmpl.Records = recs
	return &tmpl, nil
}

func (r *dnsTemplateRepo) List(ctx context.Context) ([]models.DNSTemplate, error) {
	tmpls, err := r.ListSummaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tmpls {
		recs, err := r.recordsFor(ctx, tmpls[i].ID)
		if err != nil {
			return nil, err
		}
		tmpls[i].Records = recs
	}
	return tmpls, nil
}

func (r *dnsTemplateRepo) ListSummaries(ctx context.Context) ([]models.DNSTemplate, error) {
	var tmpls []models.DNSTemplate
	err := r.db.WithContext(ctx).Order("name ASC").Find(&tmpls).Error
	if err != nil {
		return nil, err
	}
	return tmpls, nil
}

func (r *dnsTemplateRepo) Update(ctx context.Context, tmpl *models.DNSTemplate) error {
	now := time.Now().UTC()
	tmpl.UpdatedAt = now
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.DNSTemplate{}).
			Where("id = ?", tmpl.ID).
			Updates(map[string]any{
				"name":        tmpl.Name,
				"description": tmpl.Description,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		// Replace the record set wholesale — delete-all then re-insert. Editing
		// a template does NOT rewrite any zone that was already seeded from it
		// (records are one-shot); it only changes what a FUTURE create seeds.
		if err := tx.Where("template_id = ?", tmpl.ID).Delete(&models.DNSTemplateRecord{}).Error; err != nil {
			return err
		}
		return insertTemplateRecords(tx, tmpl.ID, tmpl.Records, now)
	})
}

func (r *dnsTemplateRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.DNSTemplate{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *dnsTemplateRepo) recordsFor(ctx context.Context, templateID string) ([]models.DNSTemplateRecord, error) {
	var recs []models.DNSTemplateRecord
	err := r.db.WithContext(ctx).
		Where("template_id = ?", templateID).
		Order("sort_order ASC, created_at ASC").
		Find(&recs).Error
	if err != nil {
		return nil, err
	}
	return recs, nil
}

// insertTemplateRecords stamps and inserts the record set inside the caller's
// transaction. The caller is responsible for having assigned each record's ID
// (the API stamps a ULID per record); SortOrder is (re)assigned by slice index
// so the stored order is the order the admin submitted.
func insertTemplateRecords(tx *gorm.DB, templateID string, recs []models.DNSTemplateRecord, now time.Time) error {
	for i := range recs {
		recs[i].TemplateID = templateID
		recs[i].SortOrder = i
		if recs[i].CreatedAt.IsZero() {
			recs[i].CreatedAt = now
		}
		recs[i].UpdatedAt = now
		if err := tx.Create(&recs[i]).Error; err != nil {
			return err
		}
	}
	return nil
}
