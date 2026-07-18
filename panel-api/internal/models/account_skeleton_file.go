package models

import "time"

// AccountSkeletonFile is one file in the server-wide account skeleton — the
// tree of starter files copied into every new domain's web docroot on
// creation (GH #465). Addressed by RelPath (relative to the docroot).
// Content is raw bytes so binary assets (favicons, images) round-trip
// intact. An empty table means "no skeleton" — the pre-#465 behaviour.
//
// Files are always written into the docroot as <user>:www-data 0644 with
// setgid parent dirs (the docroot ownership rule); there is no per-file
// mode in v1.
type AccountSkeletonFile struct {
	ID        string    `gorm:"column:id;primaryKey;type:char(26)" json:"id"`
	RelPath   string    `gorm:"column:rel_path;type:varchar(512);not null;uniqueIndex:uniq_skel_rel_path" json:"rel_path"`
	Content   []byte    `gorm:"column:content;type:longblob;not null" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6)" json:"updated_at"`
}

func (AccountSkeletonFile) TableName() string { return "account_skeleton_files" }

// AccountSkeletonCaps bound the skeleton so it can't be abused as bulk
// storage or blow up a new-account provision. Enforced at the API boundary.
// Path validation lives in the repo-root internal/skelpath package so the
// agent (which cannot import panel-api/internal/models) enforces the same rule.
const (
	AccountSkeletonMaxFileBytes  = 1 << 20        // 1 MiB per file
	AccountSkeletonMaxTotalBytes = 20 * (1 << 20) // 20 MiB total
	AccountSkeletonMaxFiles      = 200
)
