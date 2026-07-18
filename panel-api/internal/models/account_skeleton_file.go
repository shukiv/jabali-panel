package models

import (
	"errors"
	"path"
	"strings"
	"time"
)

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
const (
	AccountSkeletonMaxFileBytes  = 1 << 20        // 1 MiB per file
	AccountSkeletonMaxTotalBytes = 20 * (1 << 20) // 20 MiB total
	AccountSkeletonMaxFiles      = 200
	AccountSkeletonMaxPathLen    = 512
)

// ValidateSkeletonPath enforces that a skeleton entry is a safe, normalized
// relative path — no traversal, no absolute path, no backslash/NUL, no
// empty/./.. segments. Shared by the admin API (on write) and the agent (on
// apply) so both ends reject the same set; the docroot copy also does a
// filepath.Rel containment check as a belt.
func ValidateSkeletonPath(p string) error {
	switch {
	case p == "":
		return errors.New("path required")
	case len(p) > AccountSkeletonMaxPathLen:
		return errors.New("path too long")
	case strings.ContainsRune(p, 0):
		return errors.New("path contains NUL")
	case strings.ContainsRune(p, '\\'):
		return errors.New("path must use forward slashes")
	case strings.HasPrefix(p, "/"):
		return errors.New("path must be relative (no leading slash)")
	case path.Clean(p) != p:
		return errors.New("path must be normalized (no ./, ../, //, or trailing slash)")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("path contains an empty or . / .. segment")
		}
	}
	return nil
}
