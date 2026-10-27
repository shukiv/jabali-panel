package models

import (
	"strconv"
	"time"
)

// DockerApp represents an installed catalog app (M48). One row per
// install. The full catalog is admin-curated and lives under
// install/docker-apps/<slug>/ in the panel repo; `slug` references the
// catalog entry, `catalog_version` pins the version of that entry at
// install time so a panel upgrade that changes the catalog doesn't
// silently re-render an existing install with new defaults.
//
// Lifecycle (status column):
//
//	pending     -> reconciler will dispatch docker_app.install
//	installing  -> agent is rendering compose + docker compose up
//	running     -> healthchecks green; nginx vhost wired (when applicable)
//	stopped     -> docker compose down; volumes preserved
//	failed      -> install/update/start failed; last_error populated
//	updating    -> docker_app.update is in flight
//	rolling_back -> update failed; restic restore in progress
//	deleted     -> tombstone; rows linger for audit, reconciler ignores
//
// The state machine is owned by reconcileDockerApps in
// panel-api/internal/reconciler (Phase 3).
type DockerApp struct {
	ID string `gorm:"type:char(26);primaryKey" json:"id"`
	// UserID owns this install. NULL = admin / server-level (M48 behaviour).
	// Set = tenant-owned (M49): every tenant verb filters on it; ON DELETE
	// CASCADE so a user delete drops the row (host teardown is enqueued by the
	// delete path before the cascade fires).
	UserID *string `gorm:"column:user_id;type:char(26);null" json:"user_id,omitempty"`
	Slug   string  `gorm:"type:varchar(64);not null" json:"slug"`
	// InstanceSlug is the per-install identity used by the agent
	// for the on-disk directory (/var/lib/jabali/docker-apps/<x>/),
	// the docker container name (jabali-app-<x>) and restic tags
	// (slug:<x>). `Slug` stays the catalog reference (template
	// resolution); InstanceSlug is what makes multi-install of the
	// same catalog entry work without colliding paths/containers.
	// Backfilled to equal `Slug` on existing rows; new installs set
	// it to "<slug>-<name>".
	InstanceSlug   string  `gorm:"column:instance_slug;type:varchar(64);not null" json:"instance_slug"`
	Name           string  `gorm:"type:varchar(255);not null" json:"name"`
	CatalogVersion string  `gorm:"column:catalog_version;type:varchar(64);not null" json:"catalog_version"`
	ImageSHA       *string `gorm:"column:image_sha;type:varchar(128);null" json:"image_sha"`
	// AvailableDigest is the latest upstream digest the panel-api
	// observed for this app's image_channel. Differs from ImageSHA
	// when an update is available.
	AvailableDigest *string `gorm:"column:available_digest;type:varchar(128);null" json:"available_digest"`
	// LastCheckAt gates the per-app poll cadence (~6h default).
	LastCheckAt *time.Time `gorm:"column:last_check_at;type:timestamp;null" json:"last_check_at"`
	// BackupDestinationID picks which M30 backup destination this
	// app's snapshots land in. NULL falls back to env-var config
	// (Phase 8.1). ON DELETE SET NULL to avoid widow rows.
	BackupDestinationID *string    `gorm:"column:backup_destination_id;type:char(26);null" json:"backup_destination_id"`
	Status              string     `gorm:"type:varchar(32);not null;default:'pending'" json:"status"`
	UpdateMode          string     `gorm:"column:update_mode;type:varchar(16);not null;default:'manual'" json:"update_mode"`
	CPULimit            *string    `gorm:"column:cpu_limit;type:varchar(16);null" json:"cpu_limit"`
	MemoryLimit         *string    `gorm:"column:memory_limit;type:varchar(16);null" json:"memory_limit"`
	PIDsLimit           *int       `gorm:"column:pids_limit;type:int;null" json:"pids_limit"`
	LastError           *string    `gorm:"column:last_error;type:text;null" json:"last_error"`
	DataBytes           int64      `gorm:"column:data_bytes;type:bigint;not null;default:0" json:"data_bytes"`
	SizeCheckedAt       *time.Time `gorm:"column:size_checked_at;type:timestamp;null" json:"size_checked_at"`
	CreatedAt           time.Time  `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt           time.Time  `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP;onUpdate:CURRENT_TIMESTAMP" json:"updated_at"`
}

// EffectiveSlug is the on-disk / agent identifier for this install:
// instance_slug when set (UI installs + post-000160 rows), else the
// catalog slug (legacy CLI installs left instance_slug empty). Use this
// anywhere a slug is passed to the agent or used to build a data path.
func (a *DockerApp) EffectiveSlug() string {
	if a.InstanceSlug != "" {
		return a.InstanceSlug
	}
	return a.Slug
}

func (DockerApp) TableName() string { return "docker_apps" }

// DockerAppStatus values. Centralised so callers don't sprinkle string
// literals — typo'd statuses are silent reconciler skips.
const (
	DockerAppStatusPending     = "pending"
	DockerAppStatusInstalling  = "installing"
	DockerAppStatusRunning     = "running"
	DockerAppStatusStopped     = "stopped"
	DockerAppStatusFailed      = "failed"
	DockerAppStatusUpdating    = "updating"
	DockerAppStatusRollingBack = "rolling_back"
	DockerAppStatusDeleted     = "deleted"
)

// DockerAppUpdateMode values.
const (
	DockerAppUpdateModeManual = "manual"
	DockerAppUpdateModeAuto   = "auto"
)

// DockerAppPublishedPort is one published-port row per installed app.
// Catalog declares the universe of exposable ports; the install drawer
// creates one row per port the admin enables. See ADR-0116 Decision 5.
//
// BindInterface values:
//
//	loopback              -> bound to 127.0.0.1:host_port; nginx
//	                         reverse-proxy attaches when reverse_proxy=1
//	public:<managed_ip_id> -> bound to <managed_ip address>:host_port;
//	                          reachable from the public internet, no
//	                          nginx vhost (UDP, raw-TCP like Gitea SSH)
//
// Uniqueness on (bind_interface, host_port, protocol) is enforced at
// the DB level (uniq_dapp_pubport_global) so the auto-allocator can
// rely on INSERT-fail-on-conflict instead of pre-seeding a pool table.
type DockerAppPublishedPort struct {
	ID            string    `gorm:"type:char(26);primaryKey" json:"id"`
	AppID         string    `gorm:"column:app_id;type:char(26);not null" json:"app_id"`
	PortName      string    `gorm:"column:port_name;type:varchar(32);not null" json:"port_name"`
	ContainerPort int       `gorm:"column:container_port;type:int;not null" json:"container_port"`
	BindInterface string    `gorm:"column:bind_interface;type:varchar(64);not null;default:'loopback'" json:"bind_interface"`
	HostPort      int       `gorm:"column:host_port;type:int;not null" json:"host_port"`
	Protocol      string    `gorm:"type:varchar(8);not null;default:'tcp'" json:"protocol"`
	ReverseProxy  bool      `gorm:"column:reverse_proxy;type:tinyint(1);not null;default:1" json:"reverse_proxy"`
	Enabled       bool      `gorm:"type:tinyint(1);not null;default:1" json:"enabled"`
	CreatedAt     time.Time `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
}

func (DockerAppPublishedPort) TableName() string { return "docker_app_published_ports" }

// ProxyTarget is the URL a domain's proxy_pass rule sends the port's
// requests to.
func (p *DockerAppPublishedPort) ProxyTarget() string {
	return "http://127.0.0.1:" + strconv.Itoa(p.HostPort)
}

// WithoutProxyPassTo returns the rules minus the proxy_pass rules that send
// requests to target.
func (n NginxRules) WithoutProxyPassTo(target string) NginxRules {
	var kept NginxRules
	for _, r := range n {
		if r.Type == "proxy_pass" && r.Target == target {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// DockerAppBackup is a restic snapshot row for a docker app. Snapshots
// take the contents of /var/lib/jabali/docker-apps/<slug> at the point
// of capture; reason distinguishes pre-update auto-snapshots from
// operator-triggered ones so retention policy can prune the auto rows
// more aggressively.
type DockerAppBackup struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	AppID     string    `gorm:"column:app_id;type:char(26);not null" json:"app_id"`
	ResticID  string    `gorm:"column:restic_id;type:varchar(128);not null" json:"restic_id"`
	SizeBytes int64     `gorm:"column:size_bytes;type:bigint;not null" json:"size_bytes"`
	Reason    string    `gorm:"type:varchar(32);not null;default:'manual'" json:"reason"`
	CreatedAt time.Time `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
}

func (DockerAppBackup) TableName() string { return "docker_app_backups" }
