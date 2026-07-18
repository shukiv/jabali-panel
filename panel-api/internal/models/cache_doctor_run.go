package models

import (
	"encoding/json"
	"time"
)

// CacheDoctorRun records one fleet cache-maintenance pass across all
// cache-enabled WordPress installs (JAB-11). A sweep is long (one agent call
// per install), so it runs in a background goroutine and the panel polls this
// row for progress + a durable history — the same async-run shape as
// CacheWarmupRun.
type CacheDoctorRun struct {
	ID          string          `gorm:"type:char(26);primaryKey" json:"id"`
	Kind        string          `gorm:"type:varchar(16);not null" json:"kind"` // doctor|repair|refresh
	State       string          `gorm:"type:varchar(16);not null;default:queued" json:"state"`
	TriggeredBy string          `gorm:"column:triggered_by;type:varchar(26);not null;default:''" json:"triggered_by"`
	Total       int             `gorm:"not null;default:0" json:"total"`     // cache-enabled installs to process
	Checked     int             `gorm:"not null;default:0" json:"checked"`   // installs health-checked
	Drifted     int             `gorm:"not null;default:0" json:"drifted"`   // found unhealthy while cache_enabled
	Repaired    int             `gorm:"not null;default:0" json:"repaired"`  // re-provisioned (repair kind)
	Refreshed   int             `gorm:"not null;default:0" json:"refreshed"` // plugin updated (refresh kind)
	Failed      int             `gorm:"not null;default:0" json:"failed"`    // agent/repair errors
	FirstError  string          `gorm:"column:first_error;type:varchar(1024);not null;default:''" json:"first_error"`
	Results     json.RawMessage `gorm:"type:json" json:"results,omitempty"` // []CacheDoctorItem
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// CacheDoctorItem is one install's outcome within a run, serialized into the
// Results JSON. Every failure carries the install ID + path + detail so an
// operator can act on the exact site.
type CacheDoctorItem struct {
	InstallID string `json:"install_id"`
	Path      string `json:"path"`
	Host      string `json:"host,omitempty"`
	Healthy   bool   `json:"healthy"`
	Drifted   bool   `json:"drifted"`
	Repaired  bool   `json:"repaired,omitempty"`
	Refreshed bool   `json:"refreshed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Cache-doctor run kinds.
const (
	CacheDoctorKindDoctor  = "doctor"  // detect drift only (read-only)
	CacheDoctorKindRepair  = "repair"  // detect + re-provision drifted installs
	CacheDoctorKindRefresh = "refresh" // update the jabali-cache plugin fleet-wide
)

// Cache-doctor run states (shared vocabulary with warmup runs).
const (
	CacheDoctorStateQueued  = "queued"
	CacheDoctorStateRunning = "running"
	CacheDoctorStateDone    = "done"
	CacheDoctorStateFailed  = "failed"
)

func (CacheDoctorRun) TableName() string { return "cache_doctor_runs" }
