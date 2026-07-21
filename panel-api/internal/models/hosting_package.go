package models

import (
	"strings"
	"time"
)

// HostingPackage defines a quota bundle that admins assign to hosting users.
// Quotas are soft limits enforced by the agent at provisioning time and
// checked by periodic sync jobs. The panel stores them; the agent enforces.
type HostingPackage struct {
	ID   string `gorm:"type:char(26);primaryKey" json:"id"`
	Name string `gorm:"type:varchar(100);uniqueIndex:ux_packages_name;not null" json:"name"`

	// Quotas — zero means unlimited for that resource.
	DiskQuotaMB uint32 `gorm:"type:int unsigned;not null;default:0" json:"disk_quota_mb"`
	// Resource limits (M18). Enforced via POSIX user quota (disk) +
	// cgroups v2 drop-in on the per-user slice (cpu/memory/io/tasks).
	// Zero = unlimited for every field — the agent omits the systemd
	// directive entirely rather than emitting "CPUQuota=0%".
	CPUQuotaPercent  uint32 `gorm:"type:int unsigned;not null;default:0" json:"cpu_quota_percent"`
	MemoryLimitMB    uint32 `gorm:"type:int unsigned;not null;default:0" json:"memory_limit_mb"`
	IOReadMbps       uint32 `gorm:"type:int unsigned;not null;default:0" json:"io_read_mbps"`
	IOWriteMbps      uint32 `gorm:"type:int unsigned;not null;default:0" json:"io_write_mbps"`
	MaxTasks         uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_tasks"`
	BandwidthQuotaMB uint32 `gorm:"type:int unsigned;not null;default:0" json:"bandwidth_quota_mb"`
	MaxDomains       uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_domains"`
	MaxEmailAccounts uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_email_accounts"`
	MaxDatabases     uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_databases"`
	MaxDatabaseUsers uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_database_users"`
	// MaxDockerApps caps simultaneous tenant docker-app installs for users on
	// this plan. 0 = docker apps NOT included (safe default, opt-in per plan).
	MaxDockerApps uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_docker_apps"`

	// MaxPythonApps caps simultaneous tenant python-app installs for users on
	// this plan (Gitea #491). 0 = python apps NOT included (safe default,
	// opt-in per plan), mirroring MaxDockerApps.
	MaxPythonApps uint32 `gorm:"type:int unsigned;not null;default:0" json:"max_python_apps"`

	// DockerAppSlugs (GH #170 #3) is a CSV allowlist of catalog slugs a tenant
	// on this package may install. Empty = fall back to the server-wide
	// docker_tenant_apps curation. Always AND-ed with MaxDockerApps>0 +
	// tenant_installable.
	DockerAppSlugs string `gorm:"column:docker_app_slugs;type:varchar(2000);not null;default:''" json:"docker_app_slugs"`

	// Backup entitlement (GH #454). MaxBackups is the tenant retention cap
	// (max snapshots kept); 0 = tenant backups NOT included on this plan (safe
	// default, opt-in per plan, mirrors MaxDockerApps). The whole tenant backup
	// UI is gated on MaxBackups > 0. ScheduledBackupsEnabled gates the tenant
	// scheduled-backup toggle — the admin owns the schedule TIME, the tenant
	// owns the content within these limits. AllowedBackupDestinationKinds is a
	// CSV of BackupDestinationKind* a tenant on this package may target; empty
	// = none allowed.
	MaxBackups                    uint32 `gorm:"column:max_backups;type:int unsigned;not null;default:0" json:"max_backups"`
	ScheduledBackupsEnabled       bool   `gorm:"column:scheduled_backups_enabled;type:tinyint(1);not null;default:0" json:"scheduled_backups_enabled"`
	AllowedBackupDestinationKinds string `gorm:"column:allowed_backup_destination_kinds;type:varchar(255);not null;default:''" json:"allowed_backup_destination_kinds"`
	// BackupRetentionPolicy (GH #454) chooses the behaviour when a tenant hits
	// MaxBackups: "reject" (default, safe) blocks the new backup and notifies;
	// "prune" auto-forgets the oldest OWNED backup to make room, then creates,
	// and notifies. Admin-owned, per package.
	BackupRetentionPolicy string `gorm:"column:backup_retention_policy;type:varchar(16);not null;default:'reject'" json:"backup_retention_policy"`

	// Feature toggles.
	SSHEnabled bool `gorm:"type:tinyint(1);not null;default:0" json:"ssh_enabled"`
	CGIEnabled bool `gorm:"type:tinyint(1);not null;default:0" json:"cgi_enabled"`

	// PHPExecEnabled (GH #402) opts pools on this package OUT of the #401
	// disable_functions command-exec lockdown — emits no disable_functions
	// line so exec/proc_open/shell_exec/... work for apps that need them.
	// Admin-only (packages are admin-assigned); default 0 keeps the lockdown.
	PHPExecEnabled bool `gorm:"column:php_exec_enabled;type:tinyint(1);not null;default:0" json:"php_exec_enabled"`

	// PHP-FPM performance tiers (GH #339 phase 2). Per-package policy for the
	// tiered pool tuning: FpmUserCanEdit gates the L1 "Performance Mode"
	// dropdown; FpmAdvancedMode gates the L2 clamped pm.* knobs (implies
	// FpmUserCanEdit). FpmMaxChildrenCap bounds any tenant-produced
	// pm_max_children; FpmWorkerMemMb drives the advisory memory-impact budget
	// (cap * mem). FpmVersionDefaults is a JSON map {"8.3":{pm_mode,...}} of the
	// pm.* a fresh pool on this package gets, empty {} = the global mode default.
	FpmMaxChildrenCap  uint32 `gorm:"column:fpm_max_children_cap;type:int unsigned;not null;default:20" json:"fpm_max_children_cap"`
	FpmWorkerMemMb     uint32 `gorm:"column:fpm_worker_mem_mb;type:int unsigned;not null;default:64" json:"fpm_worker_mem_mb"`
	FpmUserCanEdit     bool   `gorm:"column:fpm_user_can_edit;type:tinyint(1);not null;default:0" json:"fpm_user_can_edit"`
	FpmAdvancedMode    bool   `gorm:"column:fpm_advanced_mode;type:tinyint(1);not null;default:0" json:"fpm_advanced_mode"`
	FpmVersionDefaults string `gorm:"column:fpm_version_defaults;type:varchar(2000);not null;default:'{}'" json:"fpm_version_defaults"`

	// NspawnImageVersion (M13 / ADR-0067) pins users on this package to a
	// specific systemd-nspawn rootfs at /var/lib/jabali-nspawn/images/<v>/.
	// NULL → reconciler stamps from server_settings.default_nspawn_image_version
	// at next sweep. Only takes effect when ssh_sandbox_mode=nspawn AND the
	// package has ssh_enabled=true.
	NspawnImageVersion *string `gorm:"type:varchar(64);column:nspawn_image_version" json:"nspawn_image_version,omitempty"`

	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (HostingPackage) TableName() string { return "hosting_packages" }

// BackupsEnabled reports whether tenants on this package may use backups at all
// (MaxBackups > 0). The tenant backup UI + API are gated on this (GH #454).
func (p HostingPackage) BackupsEnabled() bool { return p.MaxBackups > 0 }

// AllowedBackupKinds parses AllowedBackupDestinationKinds (CSV) into a deduped,
// lower-cased, order-preserving slice. Empty when none are allowed.
func (p HostingPackage) AllowedBackupKinds() []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, part := range strings.Split(p.AllowedBackupDestinationKinds, ",") {
		k := strings.ToLower(strings.TrimSpace(part))
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// AllowsBackupKind reports whether a tenant on this package may target the given
// backup destination kind. Always false when backups are disabled.
func (p HostingPackage) AllowsBackupKind(kind string) bool {
	if !p.BackupsEnabled() {
		return false
	}
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return false
	}
	for _, a := range p.AllowedBackupKinds() {
		if a == k {
			return true
		}
	}
	return false
}

// Backup retention policies (GH #454).
const (
	BackupRetentionReject = "reject"
	BackupRetentionPrune  = "prune"
)

// IsValidBackupRetentionPolicy validates the admin-supplied policy so a bad
// value can't reach the DB / the create gate. The empty string is accepted and
// treated as the safe "reject" default.
func IsValidBackupRetentionPolicy(s string) bool {
	switch s {
	case "", BackupRetentionReject, BackupRetentionPrune:
		return true
	}
	return false
}

// BackupRetentionPrunes reports whether this package auto-prunes the oldest
// backup at the cap. Anything other than an explicit "prune" (including the
// empty string / an unknown value) means reject — fail safe, never auto-delete
// tenant data on a misconfiguration.
func (p HostingPackage) BackupRetentionPrunes() bool {
	return p.BackupRetentionPolicy == BackupRetentionPrune
}
