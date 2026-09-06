// Package notifchannelops holds the transport-neutral owner policy for
// tenant-owned notification channels, so every adapter that can create one —
// the tenant HTTP handler and the operator CLI — enforces the same rules
// instead of each re-implementing (or skipping) them.
//
// The policy has four parts, all of which restrict a tenant-owned channel:
//
//   - the master gate (ServerSettings.TenantNotificationsEnabled),
//   - the admin-configurable kind allowlist,
//   - the per-user channel quota, and
//   - email forcing: a tenant email channel may only deliver to the owner's own
//     account address over local submission — never an arbitrary destination
//     (open relay) or a custom SMTP host (SSRF).
//
// The package intentionally does NOT import internal/api: name validation and
// per-kind config validation live there, and each adapter composes them around
// this policy (name → tenant policy → kind config → persist). It depends only
// on the models package.
package notifchannelops

import (
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MaxTenantChannelsPerUser bounds how many channels one tenant may own.
const MaxTenantChannelsPerUser = 10

// Sentinel errors. Adapters map these to their own transport (the HTTP handler
// to specific status codes + body strings, the CLI to a returned error).
var (
	// ErrTenantNotificationsDisabled means the master gate is off, so no
	// tenant-owned channel may be created at all.
	ErrTenantNotificationsDisabled = errors.New("tenant notifications are disabled on this server")
	// ErrKindNotAllowed means the channel kind is not in the admin allowlist for
	// tenant channels.
	ErrKindNotAllowed = errors.New("channel kind is not permitted for tenant channels on this server")
	// ErrNoAccountEmail means an email channel was requested but the owner has no
	// account address to force delivery to.
	ErrNoAccountEmail = errors.New("owner account has no email address to deliver to")
	// ErrTooManyChannels means the owner is already at MaxTenantChannelsPerUser.
	ErrTooManyChannels = errors.New("tenant channel limit reached")
)

// ForceOwnEmailConfig rewrites an email channel's config so it can only deliver
// to the owner's own account address over local submission. It hard-sets the
// destination + transport to the safe own-account/local values and clears any
// caller-supplied SMTP host/credentials, preserving only the non-destination
// display/formatting fields already on cfg. ownerEmail is the owner's account
// address; an empty ownerEmail yields ErrNoAccountEmail (never a partially
// forced config). This is the sole guard on the destination — nothing re-forces
// it at delivery time — so it must run on every create and every edit of a
// tenant email channel.
func ForceOwnEmailConfig(ownerEmail string, cfg *models.NotificationChannelConfig) error {
	if ownerEmail == "" {
		return ErrNoAccountEmail
	}
	cfg.ToEmail = ownerEmail
	cfg.FromEmail = ownerEmail
	cfg.SMTPMode = "local"
	cfg.SMTPHost = ""
	cfg.SMTPPort = 0
	cfg.SMTPTLS = ""
	cfg.SMTPUsername = ""
	cfg.SMTPPassword = ""
	return nil
}

// CheckKindAllowed enforces the two checks that must precede any other work on
// a tenant-owned channel: the master gate and the admin kind allowlist. A nil
// or gate-off ServerSettings fails closed with ErrTenantNotificationsDisabled;
// a kind outside the allowlist yields ErrKindNotAllowed. It runs BEFORE the
// owner's email is resolved so a disallowed kind is rejected without touching
// the user repository (the ordering the tenant HTTP handler already relied on).
func CheckKindAllowed(st *models.ServerSettings, kind string) error {
	if st == nil || !st.TenantNotificationsEnabled {
		return ErrTenantNotificationsDisabled
	}
	if !st.TenantNotificationKinds.OrDefault().Allows(kind) {
		return fmt.Errorf("%w: %s", ErrKindNotAllowed, kind)
	}
	return nil
}

// CheckQuota enforces the per-user channel quota. existingCount is how many
// channels the owner already has (from the channel repository's ListByUser).
func CheckQuota(existingCount int) error {
	if existingCount >= MaxTenantChannelsPerUser {
		return ErrTooManyChannels
	}
	return nil
}

// The three checks compose, in this order, into the full create-time owner
// policy: CheckKindAllowed → (email kinds) ForceOwnEmailConfig → per-kind config
// validation (the adapter's own) → CheckQuota. Both the tenant HTTP handler and
// the operator CLI run all three so a value one adapter refuses can't be
// smuggled in through the other. Email forcing must run before per-kind config
// validation so the forced destination fields are what gets validated and
// persisted, and after the allowlist so a disallowed kind never triggers the
// owner-email lookup.
