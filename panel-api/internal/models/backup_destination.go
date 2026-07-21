// M30.1 backup destinations (ADR-0078). Schema in migrations
// 000086 (base) + 000091 (extra_options JSON).
// Admin-managed remotes for restic copy. Credentials live in env
// files at /etc/jabali-panel/restic-remotes/<id>.env (root:root 0600);
// the DB carries only the file pointer plus a typed-options blob.
package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	BackupDestinationKindLocal = "local"
	BackupDestinationKindSFTP  = "sftp"
	BackupDestinationKindS3    = "s3"
	BackupDestinationKindB2    = "b2"
	BackupDestinationKindAzure = "azure"
	BackupDestinationKindGCS   = "gcs"
	BackupDestinationKindREST  = "rest"
)

// AllBackupDestinationKinds is the canonical list, used by REST validation
// and the admin UI dropdown. Order matters for the UI menu.
var AllBackupDestinationKinds = []string{
	BackupDestinationKindLocal,
	BackupDestinationKindSFTP,
	BackupDestinationKindS3,
	BackupDestinationKindB2,
	BackupDestinationKindAzure,
	BackupDestinationKindGCS,
	BackupDestinationKindREST,
}

// IsValidBackupDestinationKind reports whether kind is a known destination kind.
func IsValidBackupDestinationKind(kind string) bool {
	for _, k := range AllBackupDestinationKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// NormalizeBackupKindsCSV validates + canonicalises a comma-separated list of
// backup destination kinds (GH #454, hosting_packages.allowed_backup_destination
// _kinds): lower-cased, trimmed, deduped, order-preserving. Returns an error on
// any unknown kind so the API rejects bad input rather than silently storing it.
// The empty string is valid (no kinds allowed).
func NormalizeBackupKindsCSV(csv string) (string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, part := range strings.Split(csv, ",") {
		k := strings.ToLower(strings.TrimSpace(part))
		if k == "" {
			continue
		}
		if !IsValidBackupDestinationKind(k) {
			return "", fmt.Errorf("unknown backup destination kind %q (valid: %s)", k, strings.Join(AllBackupDestinationKinds, ", "))
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return strings.Join(out, ","), nil
}

// SFTPAuthKey + SFTPAuthPassword are the values BackupDestinationExtraOptions.SFTP.Auth
// can carry. Default = key (SSH key in /root/.ssh/, picked from a dropdown
// or generated via the admin's "new key" button).
const (
	SFTPAuthKey      = "key"
	SFTPAuthPassword = "password"
)

// BackupDestinationExtraOptions is the typed view of extra_options.
// Marshaled to/from the JSON column verbatim. Wrapper helpers in
// internal/backup translate this into restic `-o key=value` flags.
type BackupDestinationExtraOptions struct {
	SFTP *SFTPOptions `json:"sftp,omitempty"`
}

// SFTPOptions are the host/user/port/path + auth picker fields the
// admin UI exposes for sftp destinations. URL composition (`sftp:user@host:/path`)
// happens in the REST handler; the wrapper consults Auth + KeyPath to
// build `-o sftp.command=...` when the operator picks a non-default
// key or password auth.
type SFTPOptions struct {
	Host    string `json:"host"`
	User    string `json:"user"`
	Port    int    `json:"port,omitempty"`
	Path    string `json:"path"`
	Auth    string `json:"auth"` // SFTPAuthKey | SFTPAuthPassword
	KeyPath string `json:"key_path,omitempty"`
}

type BackupDestination struct {
	ID             string  `gorm:"type:char(26);primaryKey" json:"id"`
	Name           string  `gorm:"type:varchar(64);not null;uniqueIndex:uniq_backup_dest_name" json:"name"`
	Kind           string  `gorm:"type:enum('local','sftp','s3','b2','azure','gcs','rest');not null" json:"kind"`
	URL            string  `gorm:"type:varchar(512);not null" json:"url"`
	CredentialsRef *string `gorm:"type:varchar(255)" json:"credentials_ref,omitempty"`
	// PasswordEnc is the AES-256-GCM-sealed restic repository password
	// (M30.2 — per-destination encryption rotation, ADR-pending). NULL
	// rows fall back to the legacy shared password file. Field is
	// stripped from JSON; the password is never read by the SPA.
	PasswordEnc       []byte          `gorm:"type:varbinary(512)" json:"-"`
	PasswordRotatedAt *time.Time      `gorm:"type:datetime(6)" json:"password_rotated_at,omitempty"`
	ExtraOptions      json.RawMessage `gorm:"type:json" json:"extra_options,omitempty"`
	Enabled           bool            `gorm:"not null;default:1" json:"enabled"`
	CreatedAt         time.Time       `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt         time.Time       `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (BackupDestination) TableName() string { return "backup_destinations" }

// ExtraOptionsTyped returns the parsed view of d.ExtraOptions. Empty
// JSON yields an empty struct (no error) so callers can chain
// `.SFTP != nil` checks without nil-guarding the typed call.
func (d *BackupDestination) ExtraOptionsTyped() BackupDestinationExtraOptions {
	var out BackupDestinationExtraOptions
	if len(d.ExtraOptions) == 0 {
		return out
	}
	_ = json.Unmarshal(d.ExtraOptions, &out)
	return out
}
