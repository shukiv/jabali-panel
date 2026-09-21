package models

import "time"

// CRSHostMode is an operator-set per-host AppSec mode (GH #1641). A host with a
// row of mode "detect" is put into detection-only: CRS rules still run and score
// (explain keeps working), only the anomaly-score block is suppressed for that
// host. Host is unique — a host has at most one mode. The rendering + safety
// rules live in appseccfg.ValidateHostMode / appseccfg.RenderHostModes; unlike a
// per-path exclusion this deliberately drops the anomaly-score blockers, so it is
// its own surface with its own validator.
type CRSHostMode struct {
	ID        string    `gorm:"column:id;primaryKey;type:varchar(26)"                json:"id"`
	Host      string    `gorm:"column:host;type:varchar(253);not null;uniqueIndex:uq_crs_host_mode" json:"host"`
	Mode      string    `gorm:"column:mode;type:varchar(16);not null"                json:"mode"`
	Note      string    `gorm:"column:note;type:varchar(512);not null"               json:"note"`
	CreatedAt time.Time `gorm:"column:created_at"                                    json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"                                    json:"updated_at"`
}

func (CRSHostMode) TableName() string { return "crs_host_modes" }
