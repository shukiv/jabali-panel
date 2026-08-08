package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// PackageRepository defines data access for hosting packages.
type PackageRepository interface {
	Create(ctx context.Context, p *models.HostingPackage) error
	FindByID(ctx context.Context, id string) (*models.HostingPackage, error)
	FindByName(ctx context.Context, name string) (*models.HostingPackage, error)
	List(ctx context.Context, opts ListOptions) ([]models.HostingPackage, int64, error)
	Update(ctx context.Context, p *models.HostingPackage) error
	Delete(ctx context.Context, id string) error

	// EnsureDefaults seeds the three default Tier 1/2/3 packages on a
	// fresh install (GH #231). No-op once any package row exists, so it
	// never fights an operator who renamed or deleted the defaults.
	EnsureDefaults(ctx context.Context) error
}

type packageRepo struct{ db *gorm.DB }

func NewPackageRepository(db *gorm.DB) PackageRepository {
	return &packageRepo{db: db}
}

func (r *packageRepo) Create(ctx context.Context, p *models.HostingPackage) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *packageRepo) FindByID(ctx context.Context, id string) (*models.HostingPackage, error) {
	var p models.HostingPackage
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *packageRepo) FindByName(ctx context.Context, name string) (*models.HostingPackage, error) {
	var p models.HostingPackage
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// packageListCols — packages only have a meaningful name field for
// search. Sort allows name/created_at. Historical default was name ASC.
var packageListCols = ListCols{
	Search:      []string{"name"},
	Sort: []string{
		"name", "disk_quota_mb", "bandwidth_quota_mb", "max_domains",
		"max_email_accounts", "max_databases", "ssh_enabled", "php_exec_enabled",
		"created_at",
	},
	DefaultSort: "name",
}

func (r *packageRepo) List(ctx context.Context, opts ListOptions) ([]models.HostingPackage, int64, error) {
	var (
		pkgs  []models.HostingPackage
		total int64
	)
	base := r.db.WithContext(ctx).Model(&models.HostingPackage{})

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, packageListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// When caller didn't ask for a sort direction, preserve the historical
	// alphabetical-ASC default rather than applying the generic DESC.
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "asc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, packageListCols)
	if err := q.Find(&pkgs).Error; err != nil {
		return nil, 0, err
	}
	return pkgs, total, nil
}

func (r *packageRepo) Update(ctx context.Context, p *models.HostingPackage) error {
	// Allowlist MUST list every column the API update handler can set, or that
	// field silently never persists (GH #170 docker_app_slugs + #402
	// php_exec_enabled + the M18 resource limits were all dropped this way).
	// See feedback_domain_update_allowlist_silent_drop.
	if err := r.db.WithContext(ctx).Model(p).Where("id = ?", p.ID).Select(
		"name", "disk_quota_mb", "bandwidth_quota_mb", "max_domains",
		"max_email_accounts", "max_databases",
		"cpu_quota_percent", "memory_limit_mb", "io_read_mbps", "io_write_mbps",
		"max_tasks", "max_docker_apps", "max_python_apps", "docker_app_slugs",
		"ssh_enabled", "cgi_enabled", "php_exec_enabled",
		// GH #339: FPM performance-policy columns were missing from the Select
		// allowlist, so GORM silently dropped them on update and the policy never
		// persisted (the "allowlist silent drop" scar again).
		"fpm_max_children_cap", "fpm_worker_mem_mb", "fpm_user_can_edit",
		"fpm_advanced_mode", "fpm_version_defaults",
		"nspawn_image_version",
		// GH #454: tenant backup entitlement columns. Added to the model + API
		// update handler but missed here, so the admin's backup-limit changes
		// reported success yet silently never persisted (reverted on reload) —
		// the allowlist silent-drop scar exactly as warned above.
		// GH #454 7B: max_backup_schedules was added later than the other four
		// and missed here too — editing "Max Backup Schedules" on a package
		// reported success but reverted on reload (same silent-drop scar).
		"max_backups", "max_backup_schedules", "scheduled_backups_enabled",
		"allowed_backup_destination_kinds", "backup_retention_policy",
		"updated_at",
	).Updates(p).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *packageRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&models.HostingPackage{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// defaultPackages are the Tier 1/2/3 starter plans seeded on first boot
// (GH #231). Values are deliberately modest, cPanel-style tiers; operators
// edit or delete them from the admin Packages page. Zero = unlimited per
// the model contract, so every limit here is an explicit non-zero cap.
func defaultPackages(now time.Time) []models.HostingPackage {
	mk := func(name string, diskMB, bwMB, cpu, memMB, tasks, domains, email, dbs, dbUsers, dockerApps uint32, ssh bool) models.HostingPackage {
		return models.HostingPackage{
			ID:               ids.NewULID(),
			Name:             name,
			DiskQuotaMB:      diskMB,
			BandwidthQuotaMB: bwMB,
			CPUQuotaPercent:  cpu,
			MemoryLimitMB:    memMB,
			MaxTasks:         tasks,
			MaxDomains:       domains,
			MaxEmailAccounts: email,
			MaxDatabases:     dbs,
			MaxDatabaseUsers: dbUsers,
			MaxDockerApps:    dockerApps,
			SSHEnabled:       ssh,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
	}
	return []models.HostingPackage{
		//      name        disk    bw      cpu  mem   tasks dom email db dbu docker ssh
		mk("Tier 1", 5_000, 50_000, 50, 512, 100, 1, 5, 1, 1, 0, false),
		mk("Tier 2", 20_000, 200_000, 100, 1024, 200, 5, 25, 5, 5, 1, false),
		mk("Tier 3", 50_000, 500_000, 200, 2048, 400, 25, 100, 25, 25, 3, true),
	}
}

func (r *packageRepo) EnsureDefaults(ctx context.Context) error {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.HostingPackage{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil // already populated — never re-seed
	}
	defaults := defaultPackages(time.Now().UTC())
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range defaults {
			if err := tx.Create(&defaults[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
