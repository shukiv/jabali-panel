// Package repository — DockerApp repository (M48 Phase 1).
//
// The handlers (Phase 4) call into this; the reconciler (Phase 3) calls
// into this. The agent never touches the DB.
package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DockerAppRepository is the data-access surface for the docker_apps,
// docker_app_published_ports, and docker_app_backups tables.
//
// Ports and backups are owned by the app (CASCADE on delete in SQL);
// the repo helpers below denormalise that relationship for callers
// that want a single round-trip.
type DockerAppRepository interface {
	// --- docker_apps -----------------------------------------------------
	Create(ctx context.Context, app *models.DockerApp) error
	FindByID(ctx context.Context, id string) (*models.DockerApp, error)
	FindBySlugName(ctx context.Context, slug, name string) (*models.DockerApp, error)
	ListAll(ctx context.Context) ([]*models.DockerApp, error)
	ListByStatus(ctx context.Context, status string) ([]*models.DockerApp, error)
	// --- M49 tenant scoping (GH #170) ---
	// ListByUserID returns a tenant's installs (newest first).
	ListByUserID(ctx context.Context, userID string) ([]*models.DockerApp, error)
	// CountByUserID counts a tenant's non-deleted installs (quota gate).
	CountByUserID(ctx context.Context, userID string) (int64, error)
	// FindByIDForUser returns the app only if owned by userID (else ErrNotFound,
	// so a tenant can't probe another tenant's app IDs).
	FindByIDForUser(ctx context.Context, id, userID string) (*models.DockerApp, error)
	// SumDataBytesByUserID totals a tenant's live docker-app disk footprint
	// (the per-user disk meter, M49).
	SumDataBytesByUserID(ctx context.Context, userID string) (int64, error)
	UpdateStatus(ctx context.Context, id, status string, lastError *string) error
	UpdateDataSize(ctx context.Context, id string, dataBytes int64) error
	UpdateImageSHA(ctx context.Context, id, imageSHA string) error
	UpdateCatalogVersion(ctx context.Context, id, version string) error
	UpdateAvailableDigest(ctx context.Context, id, digest string) error
	MarkChecked(ctx context.Context, id string) error
	Update(ctx context.Context, app *models.DockerApp) error
	Delete(ctx context.Context, id string) error

	// --- docker_app_published_ports --------------------------------------
	CreatePort(ctx context.Context, p *models.DockerAppPublishedPort) error
	ListPortsForApp(ctx context.Context, appID string) ([]*models.DockerAppPublishedPort, error)
	// ListPortsForApps batch-loads published ports for many docker apps (JAB-374).
	ListPortsForApps(ctx context.Context, appIDs []string) ([]*models.DockerAppPublishedPort, error)
	DeletePort(ctx context.Context, id string) error

	// FindFreeHostPort scans the 10000..19999 pool and returns the lowest
	// host_port not currently bound to (bindInterface, protocol). Returns
	// ErrNotFound when the pool is exhausted (which would mean ~10k
	// concurrent published ports on a single interface+protocol — never
	// going to happen in practice, but the caller should still surface
	// a clean 503 rather than a 500).
	FindFreeHostPort(ctx context.Context, bindInterface, protocol string) (int, error)
	// HostPortInUse reports whether (bindInterface, protocol, hostPort)
	// is already bound by ANOTHER docker_app install. excludeAppID lets
	// the same app keep its ports during a PATCH-driven re-render.
	HostPortInUse(ctx context.Context, bindInterface, protocol string, hostPort int, excludeAppID string) (bool, error)

	// --- docker_app_backups ----------------------------------------------
	CreateBackup(ctx context.Context, b *models.DockerAppBackup) error
	ListBackupsForApp(ctx context.Context, appID string) ([]*models.DockerAppBackup, error)
	DeleteBackup(ctx context.Context, id string) error
}

type dockerAppRepo struct{ db *gorm.DB }

// NewDockerAppRepository returns a DockerAppRepository backed by GORM.
func NewDockerAppRepository(db *gorm.DB) DockerAppRepository {
	return &dockerAppRepo{db: db}
}

// --- docker_apps ------------------------------------------------------------

func (r *dockerAppRepo) Create(ctx context.Context, app *models.DockerApp) error {
	return translate(r.db.WithContext(ctx).Create(app).Error)
}

func (r *dockerAppRepo) FindByID(ctx context.Context, id string) (*models.DockerApp, error) {
	var a models.DockerApp
	if err := r.db.WithContext(ctx).First(&a, "id = ?", id).Error; err != nil {
		return nil, translate(err)
	}
	return &a, nil
}

func (r *dockerAppRepo) FindBySlugName(ctx context.Context, slug, name string) (*models.DockerApp, error) {
	var a models.DockerApp
	if err := r.db.WithContext(ctx).
		Where("slug = ? AND name = ?", slug, name).
		First(&a).Error; err != nil {
		return nil, translate(err)
	}
	return &a, nil
}

func (r *dockerAppRepo) ListAll(ctx context.Context) ([]*models.DockerApp, error) {
	var apps []*models.DockerApp
	if err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Find(&apps).Error; err != nil {
		return nil, err
	}
	return apps, nil
}

func (r *dockerAppRepo) ListByStatus(ctx context.Context, status string) ([]*models.DockerApp, error) {
	var apps []*models.DockerApp
	if err := r.db.WithContext(ctx).
		Where("status = ?", status).
		Order("created_at ASC").
		Find(&apps).Error; err != nil {
		return nil, err
	}
	return apps, nil
}

// ListByUserID returns a tenant's installs, newest first. Excludes the
// deleted tombstone status so the tenant UI + quota count see live apps only.
func (r *dockerAppRepo) ListByUserID(ctx context.Context, userID string) ([]*models.DockerApp, error) {
	var apps []*models.DockerApp
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND status <> ?", userID, models.DockerAppStatusDeleted).
		Order("created_at DESC").Find(&apps).Error
	return apps, translate(err)
}

// CountByUserID counts a tenant's live (non-deleted) installs for the quota gate.
func (r *dockerAppRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.DockerApp{}).
		Where("user_id = ? AND status <> ?", userID, models.DockerAppStatusDeleted).
		Count(&n).Error
	return n, translate(err)
}

// FindByIDForUser returns the app only when owned by userID. A miss (wrong
// owner or absent) is ErrNotFound — no existence leak across tenants.
func (r *dockerAppRepo) FindByIDForUser(ctx context.Context, id, userID string) (*models.DockerApp, error) {
	var app models.DockerApp
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&app).Error
	if err != nil {
		return nil, translate(err)
	}
	return &app, nil
}

// SumDataBytesByUserID totals data_bytes across a tenant's live (non-deleted)
// installs — the per-user docker disk meter (M49).
func (r *dockerAppRepo) SumDataBytesByUserID(ctx context.Context, userID string) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&models.DockerApp{}).
		Where("user_id = ? AND status <> ?", userID, models.DockerAppStatusDeleted).
		Select("COALESCE(SUM(data_bytes), 0)").Scan(&total).Error
	return total, translate(err)
}

func (r *dockerAppRepo) UpdateStatus(ctx context.Context, id, status string, lastError *string) error {
	updates := map[string]interface{}{
		"status":     status,
		"updated_at": time.Now(),
	}
	// nil means "leave the previous error intact"; the empty string
	// pointer means "clear it" (transition to a success state).
	if lastError != nil {
		updates["last_error"] = *lastError
	}
	return r.db.WithContext(ctx).
		Model(&models.DockerApp{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (r *dockerAppRepo) UpdateDataSize(ctx context.Context, id string, dataBytes int64) error {
	// size is advisory; bump size_checked_at so the reconciler gates the
	// next du(1). Deliberately does NOT touch updated_at — a size refresh
	// is not a meaningful state change for watchdogs / "needs attention".
	return r.db.WithContext(ctx).
		Model(&models.DockerApp{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"data_bytes":      dataBytes,
			"size_checked_at": time.Now(),
		}).Error
}

func (r *dockerAppRepo) UpdateImageSHA(ctx context.Context, id, imageSHA string) error {
	return r.db.WithContext(ctx).
		Model(&models.DockerApp{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"image_sha":  imageSHA,
			"updated_at": time.Now(),
		}).Error
}

// UpdateCatalogVersion refreshes the stored catalog_version after an update
// re-renders the install from the current catalog template, so the UI
// label tracks the catalog instead of freezing at the install-time value.
func (r *dockerAppRepo) UpdateCatalogVersion(ctx context.Context, id, version string) error {
	return r.db.WithContext(ctx).
		Model(&models.DockerApp{}).
		Where("id = ?", id).
		Update("catalog_version", version).Error
}
func (r *dockerAppRepo) UpdateAvailableDigest(ctx context.Context, id, digest string) error {
	return r.db.WithContext(ctx).
		Model(&models.DockerApp{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"available_digest": digest,
			"updated_at":       time.Now(),
		}).Error
}

func (r *dockerAppRepo) MarkChecked(ctx context.Context, id string) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&models.DockerApp{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"last_check_at": now,
			"updated_at":    now,
		}).Error
}

func (r *dockerAppRepo) Update(ctx context.Context, app *models.DockerApp) error {
	return r.db.WithContext(ctx).Save(app).Error
}

func (r *dockerAppRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&models.DockerApp{}).Error
}

// --- docker_app_published_ports ---------------------------------------------

func (r *dockerAppRepo) CreatePort(ctx context.Context, p *models.DockerAppPublishedPort) error {
	return translate(r.db.WithContext(ctx).Create(p).Error)
}

func (r *dockerAppRepo) ListPortsForApp(ctx context.Context, appID string) ([]*models.DockerAppPublishedPort, error) {
	var ports []*models.DockerAppPublishedPort
	if err := r.db.WithContext(ctx).
		Where("app_id = ?", appID).
		Order("port_name ASC").
		Find(&ports).Error; err != nil {
		return nil, err
	}
	return ports, nil
}

// ListPortsForApps batch-loads published ports for a set of docker apps in one
// query (JAB-374). ORDER BY reproduces ListPortsForApp's port_name within each app.
func (r *dockerAppRepo) ListPortsForApps(ctx context.Context, appIDs []string) ([]*models.DockerAppPublishedPort, error) {
	if len(appIDs) == 0 {
		return nil, nil
	}
	var ports []*models.DockerAppPublishedPort
	if err := r.db.WithContext(ctx).Where("app_id IN ?", appIDs).
		Order("app_id, port_name ASC").Find(&ports).Error; err != nil {
		return nil, err
	}
	return ports, nil
}

func (r *dockerAppRepo) DeletePort(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&models.DockerAppPublishedPort{}).Error
}

// hostPortPoolMin / hostPortPoolMax bound the auto-allocator. The pool
// was deliberately chosen well above conventional service ports (8080,
// 8443, 9000-9100 metrics range) so an operator pinning a custom port
// elsewhere doesn't collide with the auto-allocated apps.
const (
	hostPortPoolMin = 10000
	hostPortPoolMax = 19999
)

func (r *dockerAppRepo) FindFreeHostPort(ctx context.Context, bindInterface, protocol string) (int, error) {
	// Pull the set of (host_port) values currently bound to this
	// (bindInterface, protocol). Sort and walk to find the lowest gap.
	var used []int
	if err := r.db.WithContext(ctx).
		Model(&models.DockerAppPublishedPort{}).
		Where("bind_interface = ? AND protocol = ?", bindInterface, protocol).
		Order("host_port ASC").
		Pluck("host_port", &used).Error; err != nil {
		return 0, err
	}
	usedSet := make(map[int]struct{}, len(used))
	for _, p := range used {
		usedSet[p] = struct{}{}
	}
	for p := hostPortPoolMin; p <= hostPortPoolMax; p++ {
		if _, taken := usedSet[p]; !taken {
			return p, nil
		}
	}
	return 0, ErrNotFound
}

// --- docker_app_backups -----------------------------------------------------

func (r *dockerAppRepo) CreateBackup(ctx context.Context, b *models.DockerAppBackup) error {
	return translate(r.db.WithContext(ctx).Create(b).Error)
}

func (r *dockerAppRepo) ListBackupsForApp(ctx context.Context, appID string) ([]*models.DockerAppBackup, error) {
	var rows []*models.DockerAppBackup
	if err := r.db.WithContext(ctx).
		Where("app_id = ?", appID).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *dockerAppRepo) DeleteBackup(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&models.DockerAppBackup{}).Error
}

// translate is the package-local error mapper from repository.go; we
// re-import the local errors here to keep the typed error contract
// uniform across the package.
var _ = errors.New

// HostPortInUse mirrors the (bind_interface, host_port, protocol) DB
// uniqueness so the install handler can surface a friendly error
// instead of letting the INSERT fail with a constraint-violation
// string the operator can't parse.
func (r *dockerAppRepo) HostPortInUse(ctx context.Context, bindInterface, protocol string, hostPort int, excludeAppID string) (bool, error) {
	var count int64
	q := r.db.WithContext(ctx).Model(&models.DockerAppPublishedPort{}).
		Where("bind_interface = ? AND protocol = ? AND host_port = ?", bindInterface, protocol, hostPort)
	if excludeAppID != "" {
		q = q.Where("app_id <> ?", excludeAppID)
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
