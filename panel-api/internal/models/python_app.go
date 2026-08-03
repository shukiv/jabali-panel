package models

import "time"

// PythonApp is a native (non-PHP) Python web app the panel runs as a per-user
// systemd service behind an nginx proxy (ADR-0131 / GH #203). Mirrors the
// DockerApp registry shape. DB-as-truth: the reconciler converges the venv,
// the jabali-app@<id>.service unit, and the per-domain nginx include.
type PythonApp struct {
	ID       string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID   string `gorm:"column:user_id;type:char(26);not null" json:"user_id"`
	DomainID string `gorm:"column:domain_id;type:char(26);not null" json:"domain_id"`
	Name     string `gorm:"type:varchar(255);not null" json:"name"`

	// Runtime is "python" for now; the enum leaves room for node/ruby later.
	Runtime string `gorm:"type:varchar(16);not null;default:'python'" json:"runtime"`
	// PythonVersion is "3.11"/"3.12" — must be one the host has installed.
	PythonVersion string `gorm:"column:python_version;type:varchar(16);not null" json:"python_version"`
	// AppRoot is a path under the owner's home (scope-validated by the agent).
	AppRoot string `gorm:"column:app_root;type:varchar(512);not null" json:"app_root"`
	// AppType is "wsgi" (gunicorn) or "asgi" (uvicorn).
	AppType string `gorm:"column:app_type;type:varchar(8);not null;default:'wsgi'" json:"app_type"`
	// Entrypoint is the module:callable, e.g. "myapp.wsgi:application".
	Entrypoint string `gorm:"type:varchar(255);not null" json:"entrypoint"`
	// BaseURI is the mount point on the domain, "/" or "/app".
	BaseURI string `gorm:"column:base_uri;type:varchar(255);not null;default:'/'" json:"base_uri"`
	// StaticURL/StaticRoot describe the static-asset split (GH #878): nginx
	// serves <base_uri><static_url> directly from <app_root>/<static_root>
	// instead of proxying to the app — the Passenger public/ equivalent.
	// Empty = everything proxies. Framework installs seed these from the
	// catalog entry.
	StaticURL  string `gorm:"column:static_url;type:varchar(255);not null" json:"static_url,omitempty"`
	StaticRoot string `gorm:"column:static_root;type:varchar(512);not null" json:"static_root,omitempty"`
	// LoopbackPort is the 127.0.0.1 port gunicorn/uvicorn binds; nginx
	// proxy_passes to it. Allocated by the API on create (nil until then).
	LoopbackPort *int `gorm:"column:loopback_port;type:int;null" json:"loopback_port,omitempty"`
	// StartCommand overrides the derived gunicorn/uvicorn command when set.
	StartCommand *string `gorm:"column:start_command;type:varchar(1024);null" json:"start_command,omitempty"`

	// Framework is the marketplace slug this app was provisioned from (JAB-164),
	// empty for hand-configured apps. When set and ScaffoldedAt is nil, the
	// reconciler runs app.python.scaffold (starter project + pinned deps) before
	// the first app.python.apply.
	Framework string `gorm:"column:framework;type:varchar(32);null" json:"framework,omitempty"`
	// ScaffoldedAt records when the framework starter was laid down. nil means the
	// one-shot scaffold still needs to run; once set the reconciler never
	// re-scaffolds over the tenant's code.
	ScaffoldedAt *time.Time `gorm:"column:scaffolded_at;type:timestamp;null" json:"scaffolded_at,omitempty"`

	Status      string  `gorm:"type:varchar(32);not null;default:'pending'" json:"status"`
	CPULimit    *string `gorm:"column:cpu_limit;type:varchar(16);null" json:"cpu_limit,omitempty"`
	MemoryLimit *string `gorm:"column:memory_limit;type:varchar(16);null" json:"memory_limit,omitempty"`
	PIDsLimit   *int    `gorm:"column:pids_limit;type:int;null" json:"pids_limit,omitempty"`
	LastError   *string `gorm:"column:last_error;type:text;null" json:"last_error,omitempty"`

	CreatedAt time.Time `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP;onUpdate:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (PythonApp) TableName() string { return "python_apps" }

// PythonAppEnv is one environment variable for a PythonApp; the reconciler
// renders these into the unit's EnvironmentFile.
type PythonAppEnv struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	AppID     string    `gorm:"column:app_id;type:char(26);not null" json:"app_id"`
	Key       string    `gorm:"column:env_key;type:varchar(255);not null" json:"key"`
	Value     string    `gorm:"column:env_value;type:text" json:"value"`
	CreatedAt time.Time `gorm:"type:timestamp;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
}

func (PythonAppEnv) TableName() string { return "python_app_env" }

// Python app status values (reconciler-owned).
const (
	PythonAppStatusPending  = "pending"
	PythonAppStatusBuilding = "building"
	PythonAppStatusRunning  = "running"
	PythonAppStatusStopped  = "stopped"
	PythonAppStatusFailed   = "failed"
)
