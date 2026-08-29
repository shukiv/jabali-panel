package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// StringList is a []string persisted to a JSON column (empty => SQL NULL).
type StringList []string

func (s StringList) Value() (driver.Value, error) {
	if len(s) == 0 {
		return nil, nil
	}
	return json.Marshal(s)
}

func (s *StringList) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("StringList.Scan: unsupported type %T", src)
	}
	if len(b) == 0 {
		*s = nil
		return nil
	}
	return json.Unmarshal(b, s)
}

// PHPPool represents a PHP-FPM pool bound to a panel user.
// Each panel user gets exactly one pool per MVP constraint.
type PHPPool struct {
	ID                             string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID                         string `gorm:"type:char(26);not null" json:"user_id"`
	PHPVersion                     string `gorm:"type:varchar(8);not null" json:"php_version"`
	PmMode                         string `gorm:"type:varchar(16);not null;default:'ondemand'" json:"pm_mode"`
	PmMaxChildren                  uint32 `gorm:"type:int unsigned;not null;default:20" json:"pm_max_children"`
	ProcessIdleTimeoutSeconds      uint32 `gorm:"type:int unsigned;not null;default:60" json:"process_idle_timeout_seconds"`
	PmStartServers                 uint32 `gorm:"column:pm_start_servers;type:int unsigned;not null;default:2" json:"pm_start_servers"`
	PmMinSpareServers              uint32 `gorm:"column:pm_min_spare_servers;type:int unsigned;not null;default:1" json:"pm_min_spare_servers"`
	PmMaxSpareServers              uint32 `gorm:"column:pm_max_spare_servers;type:int unsigned;not null;default:3" json:"pm_max_spare_servers"`
	PmMaxRequests                  uint32 `gorm:"column:pm_max_requests;type:int unsigned;not null;default:0" json:"pm_max_requests"`
	RequestTerminateTimeoutSeconds uint32 `gorm:"column:request_terminate_timeout_seconds;type:int unsigned;not null;default:0" json:"request_terminate_timeout_seconds"`
	// SlowlogTimeoutSeconds (GH #1332 item 12): when > 0, FPM logs a backtrace of
	// any request slower than this to the pool's slow log. 0 = disabled. The
	// slowlog path is derived agent-side from the slug (never sent over the wire).
	SlowlogTimeoutSeconds uint32 `gorm:"column:slowlog_timeout_seconds;type:int unsigned;not null;default:0" json:"slowlog_timeout_seconds"`
	PerformanceMode       string `gorm:"column:performance_mode;type:varchar(24);not null;default:'balanced'" json:"performance_mode"`
	// ExtraExtensions are optional PHP extensions the tenant opted this pool into
	// (GH #1332 item 16), loaded per-pool via php_admin_value[extension]. Names
	// are validated against the installed-extension allowlist (phpext). Can add
	// installed extras; cannot disable a base extension (loaded server-wide).
	ExtraExtensions StringList `gorm:"column:extra_extensions;type:json" json:"extra_extensions,omitempty"`
	Status          string     `gorm:"type:varchar(16);not null;default:'pending'" json:"status"`
	LastError       *string    `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt       time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (PHPPool) TableName() string { return "php_pools" }

// NewVersionedPHPPool builds a non-default (per-version) pool for a user by
// cloning the COMPLETE tuning model from the user's default pool. It is the one
// owner of that clone (JAB-344): the HTTP path cloned eight pm_* fields, the CLI
// path cloned only three, and NEITHER cloned performance_mode — so switching a
// domain's PHP version silently reset capacity/timeout behavior. The caller
// supplies the fresh id + version; status starts pending for the reconciler.
func NewVersionedPHPPool(id, phpVersion string, def *PHPPool) *PHPPool {
	return &PHPPool{
		ID:                             id,
		UserID:                         def.UserID,
		PHPVersion:                     phpVersion,
		PmMode:                         def.PmMode,
		PmMaxChildren:                  def.PmMaxChildren,
		ProcessIdleTimeoutSeconds:      def.ProcessIdleTimeoutSeconds,
		PmStartServers:                 def.PmStartServers,
		PmMinSpareServers:              def.PmMinSpareServers,
		PmMaxSpareServers:              def.PmMaxSpareServers,
		PmMaxRequests:                  def.PmMaxRequests,
		RequestTerminateTimeoutSeconds: def.RequestTerminateTimeoutSeconds,
		SlowlogTimeoutSeconds:          def.SlowlogTimeoutSeconds,
		PerformanceMode:                def.PerformanceMode,
		ExtraExtensions:                def.ExtraExtensions,
		Status:                         "pending",
	}
}
