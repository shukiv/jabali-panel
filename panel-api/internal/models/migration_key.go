package models

import "time"

// MigrationKey (GH #648) is the one-time credential a jabali-migrator WordPress
// plugin claims to push a source site into a destination. It is the trust
// boundary for the public claim/upload path:
//   - Only a SHA-256 hash of the key (and of the exchanged upload-session token)
//     is ever persisted — the plaintext key is shown to the operator ONCE at
//     creation and never stored.
//   - Claiming is one-time: an unclaimed key exchanges for a scoped upload
//     session token and pins the source URL; a claimed/expired/revoked key is
//     rejected.
//   - Every field the destination import needs (owner, domain, docroot) is
//     bound here at creation so the upload path never trusts client input for
//     the target.
type MigrationKey struct {
	ID    string `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	JobID string `gorm:"column:job_id;type:char(26);not null;index:idx_migration_keys_job" json:"job_id"`
	// KeyHash is hex(sha256(plaintext key)). Unique so a claim looks it up by
	// hash; the handler still does a constant-time compare, never a bare SQL eq.
	KeyHash string `gorm:"column:key_hash;type:char(64);not null;uniqueIndex:uq_migration_key_hash" json:"-"`
	// Destination binding — set at creation, owner-scoped by the API layer.
	DestUserID   string `gorm:"column:dest_user_id;type:char(26);not null" json:"dest_user_id"`
	DestDomainID string `gorm:"column:dest_domain_id;type:char(26);not null" json:"dest_domain_id"`
	DestDocroot  string `gorm:"column:dest_docroot;type:varchar(1024);not null" json:"dest_docroot"`
	DomainChange bool   `gorm:"column:domain_change;type:tinyint(1);not null;default:0" json:"domain_change"`
	// Status: unclaimed -> claimed -> active -> done, or -> revoked. See consts.
	Status    string    `gorm:"column:status;type:varchar(16);not null;default:'unclaimed';index:idx_migration_keys_status" json:"status"`
	ExpiresAt time.Time `gorm:"column:expires_at;type:datetime(6);not null" json:"expires_at"`
	// ClaimedSourceURL is NULL until claimed, then pinned — later upload calls
	// must match it (a claimed key can't be re-pointed at a different source).
	ClaimedSourceURL *string `gorm:"column:claimed_source_url;type:varchar(255)" json:"claimed_source_url,omitempty"`
	// UploadSessionHash is hex(sha256(upload session token)) — the scoped token
	// the claim returns; all upload calls authenticate with it, never the key.
	UploadSessionHash *string   `gorm:"column:upload_session_hash;type:char(64)" json:"-"`
	CreatedAt         time.Time `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (MigrationKey) TableName() string { return "migration_keys" }

// MigrationKey status lifecycle.
const (
	MigrationKeyUnclaimed = "unclaimed" // created, waiting for the plugin to claim
	MigrationKeyClaimed   = "claimed"   // claimed; source URL pinned; upload session issued
	MigrationKeyActive    = "active"    // manifest accepted; payload uploading
	MigrationKeyDone      = "done"      // upload finalized (import is a separate operator-gated step)
	MigrationKeyRevoked   = "revoked"   // operator-revoked or terminal cleanup
)

// IsClaimable reports whether the key can still be claimed (unclaimed + unexpired).
func (k *MigrationKey) IsClaimable(now time.Time) bool {
	return k != nil && k.Status == MigrationKeyUnclaimed && now.Before(k.ExpiresAt)
}
