package repository

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ApplicationInstallRepository defines data access for installed apps.
// One row per (domain, subdirectory, app_type) — see migration 000046.
// The interface is named for the M19 generalisation; the legacy alias
// `WordPressInstallRepository` (below) keeps WP-specific call sites
// compiling through the M19 release window.
type ApplicationInstallRepository interface {
	Create(ctx context.Context, install *models.ApplicationInstall) error
	FindByID(ctx context.Context, id string) (*models.ApplicationInstall, error)
	FindByIDAndUserID(ctx context.Context, id, userID string) (*models.ApplicationInstall, error)
	FindByDomainID(ctx context.Context, domainID string) (*models.ApplicationInstall, error)
	// FindByDomainAndSubdirectory enforces install uniqueness at the
	// (domain, subdirectory) granularity that matches the on-disk install
	// path. Empty subdirectory = docroot install. PRE-M19 callers used
	// this for the duplicate-install precheck; post-M19 the precheck
	// SHOULD use FindByDomainAndSubdirectoryAndAppType so two distinct
	// app types (e.g. WordPress + DokuWiki) can share a (domain, subdir).
	FindByDomainAndSubdirectory(ctx context.Context, domainID, subdirectory string) (*models.ApplicationInstall, error)
	// FindByDomainAndSubdirectoryAndAppType returns the install at the
	// exact (domain, subdir, app_type) coordinate. Use this for the
	// 409 install_exists check on POST /applications — different app
	// types in the same (domain, subdir) slot are allowed by design.
	FindByDomainAndSubdirectoryAndAppType(ctx context.Context, domainID, subdirectory, appType string) (*models.ApplicationInstall, error)
	FindByDBID(ctx context.Context, dbID string) (*models.ApplicationInstall, error)
	ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.ApplicationInstall, int64, error)
	List(ctx context.Context, opts ListOptions) ([]models.ApplicationInstall, int64, error)
	UpdateStatus(ctx context.Context, id, status string, lastError *string, version *string) error
	// UpdateCacheEnabled writes application_installs.cache_enabled (GH #406).
	UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error
	// UpdateCacheSettings writes the per-install cache_settings JSON column
	// (GH #612/#616/#618). A nil raw clears it back to defaults.
	UpdateCacheSettings(ctx context.Context, id string, raw json.RawMessage) error
	// ListCacheEnabledByDomainID returns all cache-enabled WordPress installs on
	// a domain (GH #601) — a domain can host several (/, /blog), and the nginx
	// page-cache gate must allow every one of their path prefixes.
	ListCacheEnabledByDomainID(ctx context.Context, domainID string) ([]models.ApplicationInstall, error)
	// ListAllCacheEnabled returns every cache-enabled WordPress install (admin
	// cache overview, GH #617).
	ListAllCacheEnabled(ctx context.Context) ([]models.ApplicationInstall, error)
	// CountCacheEnabledByUserID counts a user's WordPress installs with the
	// object cache ON, excluding excludeID. The per-tenant Redis ACL user
	// wp_<osuser> is shared across a tenant's installs, so it may only be
	// revoked when this returns 0 (the last install just disabled) — GH #408.
	CountCacheEnabledByUserID(ctx context.Context, userID, excludeID string) (int64, error)
	// CountCacheEnabledByDomainID counts cache-ON WordPress installs on a domain,
	// excluding excludeID. domains.cache_enabled (the nginx page cache, ADR-0108)
	// is per-domain but a domain can host multiple installs, so it may only be
	// flipped off when this returns 0 — otherwise a sibling loses page cache
	// (GH #409).
	CountCacheEnabledByDomainID(ctx context.Context, domainID, excludeID string) (int64, error)
	Delete(ctx context.Context, id string) error
	// ListReadyByUpdatedAtAsc returns ready WORDPRESS installs ordered
	// oldest-updated-first, capped to limit. The reconciler probe loop
	// uses this for round-robin fairness. It is scoped to app_type =
	// 'wordpress' because the only probe is a WordPress-specific
	// wp-includes/version.php check — non-WordPress apps (itflow,
	// dokuwiki, …) have no such file and would be falsely flagged as
	// drifted (GH #378).
	ListReadyByUpdatedAtAsc(ctx context.Context, limit int) ([]models.ApplicationInstall, error)
}

// WordPressInstallRepository is the pre-M19 alias. Same interface, kept
// so wordpress.go handler code compiles unchanged through M19. M19.1
// deletes this alias.
type WordPressInstallRepository = ApplicationInstallRepository

type applicationInstallRepo struct{ db *gorm.DB }

// NewApplicationInstallRepository constructs the GORM-backed repo.
func NewApplicationInstallRepository(db *gorm.DB) ApplicationInstallRepository {
	return &applicationInstallRepo{db: db}
}

// NewWordPressInstallRepository is the pre-M19 constructor name. Kept as
// a thin alias so app.go's wiring code compiles unchanged through M19.
func NewWordPressInstallRepository(db *gorm.DB) WordPressInstallRepository {
	return NewApplicationInstallRepository(db)
}

func (r *applicationInstallRepo) Create(ctx context.Context, install *models.ApplicationInstall) error {
	if err := r.db.WithContext(ctx).Create(install).Error; err != nil {
		return err
	}
	return nil
}

func (r *applicationInstallRepo) FindByID(ctx context.Context, id string) (*models.ApplicationInstall, error) {
	var install models.ApplicationInstall
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&install).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &install, nil
}

func (r *applicationInstallRepo) FindByIDAndUserID(ctx context.Context, id, userID string) (*models.ApplicationInstall, error) {
	var install models.ApplicationInstall
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&install).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &install, nil
}

func (r *applicationInstallRepo) FindByDomainID(ctx context.Context, domainID string) (*models.ApplicationInstall, error) {
	var install models.ApplicationInstall
	if err := r.db.WithContext(ctx).Where("domain_id = ?", domainID).First(&install).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &install, nil
}

func (r *applicationInstallRepo) FindByDomainAndSubdirectory(ctx context.Context, domainID, subdirectory string) (*models.ApplicationInstall, error) {
	var install models.ApplicationInstall
	if err := r.db.WithContext(ctx).
		Where("domain_id = ? AND subdirectory = ?", domainID, subdirectory).
		First(&install).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &install, nil
}

func (r *applicationInstallRepo) FindByDomainAndSubdirectoryAndAppType(ctx context.Context, domainID, subdirectory, appType string) (*models.ApplicationInstall, error) {
	var install models.ApplicationInstall
	if err := r.db.WithContext(ctx).
		Where("domain_id = ? AND subdirectory = ? AND app_type = ?", domainID, subdirectory, appType).
		First(&install).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &install, nil
}

func (r *applicationInstallRepo) FindByDBID(ctx context.Context, dbID string) (*models.ApplicationInstall, error) {
	var install models.ApplicationInstall
	if err := r.db.WithContext(ctx).Where("db_id = ?", dbID).First(&install).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &install, nil
}

var applicationInstallListCols = ListCols{
	Search:      []string{"admin_email"},
	Sort:        []string{"admin_email", "status", "created_at"},
	DefaultSort: "created_at",
}

func (r *applicationInstallRepo) ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.ApplicationInstall, int64, error) {
	var (
		installs []models.ApplicationInstall
		total    int64
	)
	base := r.db.WithContext(ctx).Model(&models.ApplicationInstall{}).Where("user_id = ?", userID)

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, applicationInstallListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, applicationInstallListCols)
	if err := q.Find(&installs).Error; err != nil {
		return nil, 0, err
	}
	return installs, total, nil
}

func (r *applicationInstallRepo) List(ctx context.Context, opts ListOptions) ([]models.ApplicationInstall, int64, error) {
	var (
		installs []models.ApplicationInstall
		total    int64
	)
	base := r.db.WithContext(ctx).Model(&models.ApplicationInstall{})

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, applicationInstallListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, applicationInstallListCols)
	if err := q.Find(&installs).Error; err != nil {
		return nil, 0, err
	}
	return installs, total, nil
}

func (r *applicationInstallRepo) UpdateStatus(ctx context.Context, id, status string, lastError *string, version *string) error {
	updates := map[string]interface{}{
		"status": status,
	}
	if lastError != nil {
		updates["last_error"] = *lastError
	}
	if version != nil {
		updates["version"] = *version
	}
	if err := r.db.WithContext(ctx).Model(&models.ApplicationInstall{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}

func (r *applicationInstallRepo) UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error {
	return r.db.WithContext(ctx).Model(&models.ApplicationInstall{}).
		Where("id = ?", id).Update("cache_enabled", enabled).Error
}

func (r *applicationInstallRepo) UpdateCacheSettings(ctx context.Context, id string, raw json.RawMessage) error {
	var val interface{}
	if len(raw) > 0 {
		val = []byte(raw)
	} // nil => SQL NULL (reset to defaults)
	return r.db.WithContext(ctx).Model(&models.ApplicationInstall{}).
		Where("id = ?", id).Update("cache_settings", val).Error
}

func (r *applicationInstallRepo) ListCacheEnabledByDomainID(ctx context.Context, domainID string) ([]models.ApplicationInstall, error) {
	var out []models.ApplicationInstall
	err := r.db.WithContext(ctx).
		Where("domain_id = ? AND app_type = ? AND cache_enabled = ?", domainID, "wordpress", true).
		Find(&out).Error
	return out, err
}

func (r *applicationInstallRepo) ListAllCacheEnabled(ctx context.Context) ([]models.ApplicationInstall, error) {
	var out []models.ApplicationInstall
	err := r.db.WithContext(ctx).
		Where("app_type = ? AND cache_enabled = ?", "wordpress", true).
		Find(&out).Error
	return out, err
}

func (r *applicationInstallRepo) CountCacheEnabledByUserID(ctx context.Context, userID, excludeID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.ApplicationInstall{}).
		Where("user_id = ? AND app_type = ? AND cache_enabled = ? AND id <> ?",
			userID, "wordpress", true, excludeID).
		Count(&n).Error
	return n, err
}

func (r *applicationInstallRepo) CountCacheEnabledByDomainID(ctx context.Context, domainID, excludeID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.ApplicationInstall{}).
		Where("domain_id = ? AND app_type = ? AND cache_enabled = ? AND id <> ?",
			domainID, "wordpress", true, excludeID).
		Count(&n).Error
	return n, err
}

func (r *applicationInstallRepo) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Delete(&models.ApplicationInstall{}, "id = ?", id)
	if err := result.Error; err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *applicationInstallRepo) ListReadyByUpdatedAtAsc(ctx context.Context, limit int) ([]models.ApplicationInstall, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []models.ApplicationInstall
	err := r.db.WithContext(ctx).
		Where("status = ? AND app_type = ?", "ready", "wordpress").
		Order("updated_at ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
