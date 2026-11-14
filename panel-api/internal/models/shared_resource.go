package models

import "time"

// SharedResource is a standalone shared resource (M52, ADR-0133): one
// collection — a shared mailbox, calendar, address book, or file folder —
// hosted by its own Stalwart principal and shared selectively via the
// per-collection shareWith ACL. Replaces the M51 "one group owns one of
// each, all-or-nothing" model.
//
// DB-as-truth: this row is authoritative. The reconciler projects it into a
// Stalwart host principal (sharedresource.apply) and caches the resulting
// HostAccountID + CollectionID, then converges its grants via the
// {calendar,addressbook,file,mailbox}.share_set agent commands.
//
// LocalPart / EmailCached are set only for Kind=="mailbox" (a shared mailbox
// is an SMTP recipient); NULL for the DAV-only kinds, so the UNIQUE index on
// EmailCached permits many non-mailbox rows. Unlike mail_groups, EmailCached
// is maintained in Go (no trigger), set when the row is created.
type SharedResource struct {
	ID            string     `gorm:"type:char(26);primaryKey" json:"id"`
	DomainID      string     `gorm:"type:char(26);not null;index:ix_shared_resources_domain" json:"domain_id"`
	Kind          string     `gorm:"type:enum('mailbox','calendar','addressbook','files');not null;index:ix_shared_resources_kind" json:"kind"`
	LocalPart     *string    `gorm:"type:varchar(64)" json:"local_part,omitempty"`
	EmailCached   *string    `gorm:"column:email_cached;type:varchar(320);uniqueIndex:ux_shared_resources_email" json:"email,omitempty"`
	DisplayName   string     `gorm:"column:display_name;type:varchar(255);not null;default:''" json:"display_name"`
	HostAccountID string     `gorm:"column:host_account_id;type:varchar(64);not null;default:''" json:"host_account_id"`
	CollectionID  string     `gorm:"column:collection_id;type:varchar(64);not null;default:''" json:"collection_id"`
	CreatedAt     time.Time  `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (SharedResource) TableName() string { return "shared_resources" }

// SharedResourceGrant is one grant: which mailbox or group may access a
// shared resource, at what coarse level. The grantee is polymorphic
// (mailbox|group), so it has no DB foreign key; the reconciler resolves
// grantee → target principal email(s) (a group expands to its members) and
// app code prunes grants on grantee delete. The agent maps Rights to the
// per-resource Stalwart ACL keys. "readdelete" (read and delete, nothing
// else) is for Files resources only — GH #2503 scans folders.
type SharedResourceGrant struct {
	ResourceID  string    `gorm:"column:resource_id;type:char(26);primaryKey" json:"resource_id"`
	GranteeKind string    `gorm:"column:grantee_kind;type:enum('mailbox','group');primaryKey" json:"grantee_kind"`
	GranteeID   string    `gorm:"column:grantee_id;type:char(26);primaryKey" json:"grantee_id"`
	Rights      string    `gorm:"type:enum('read','readwrite','admin','readdelete');not null;default:'read'" json:"rights"`
	CreatedAt   time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (SharedResourceGrant) TableName() string { return "shared_resource_grants" }

// SharedResourceTombstone records a deleted shared resource's host address so
// the reconciler can durably tear down the Stalwart host principal (and clear
// the tombstone on success), even if the synchronous delete-time teardown
// failed (agent down).
type SharedResourceTombstone struct {
	Email     string    `gorm:"type:varchar(320);primaryKey" json:"email"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (SharedResourceTombstone) TableName() string { return "shared_resource_tombstones" }
