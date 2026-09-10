// Package domainops is the shared Domain Lifecycle Module (JAB-279): the
// transport-neutral policy the REST handler and the operator CLI both route
// through, so the eligibility gate that decides whether an owner may acquire a
// new domain has one owner and cannot drift between the two adapters.
//
// Authorization and identity resolution stay adapter concerns (ADR-0083): every
// entry point takes an already-loaded owner. The REST handler resolves it from
// the request's OwnerID and maps a missing owner to its own 404; the CLI
// resolves it via resolveUser (email / username / ULID). This leaf only decides
// whether that owner is eligible to host a domain, and returns typed sentinels
// each adapter maps to its own transport, with no HTTP knowledge here.
package domainops

import (
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Sentinel errors. Adapters map these to their own transport — the REST handler
// to the status codes and body strings it returned before this leaf, the CLI to
// a returned error.
var (
	// ErrOwnerNil means no owner was supplied — a wiring bug, not a policy result.
	ErrOwnerNil = errors.New("domainops: owner is nil")
	// ErrAdminCannotHost means the owner is a panel-only admin. Admins have no
	// /home/<name>, so a domain cannot host under them.
	ErrAdminCannotHost = errors.New("domainops: admin users cannot host domains")
	// ErrOwnerSuspended means the owner is administratively suspended. A fresh
	// vhost would be live while the account stays locked, defeating the suspend
	// cascade, so the create must fail before allocation or persistence.
	ErrOwnerSuspended = errors.New("domainops: owner is suspended")
	// ErrOwnerNoUsername means the owner has no username — an inconsistent state
	// (a hosting user always has one). The adapter surfaces it as an internal error.
	ErrOwnerNoUsername = errors.New("domainops: owner has no username")
)

// CheckOwnerEligible reports whether owner may acquire a new domain, in the REST
// handler's original order: admin first, then suspended, then username. It
// returns nil when the owner is eligible, or one of the sentinels above.
//
// AC3 of JAB-279: suspended, admin, and missing owners must fail before any port
// allocation or persistence. This predicate is the single gate both adapters
// call before they build or persist the row, so the CLI can no longer create a
// live vhost for a suspended owner (a gap the REST handler never had).
func CheckOwnerEligible(owner *models.User) error {
	if owner == nil {
		return ErrOwnerNil
	}
	if owner.IsAdmin {
		return ErrAdminCannotHost
	}
	if owner.Suspended {
		return ErrOwnerSuspended
	}
	if owner.Username == nil || *owner.Username == "" {
		return ErrOwnerNoUsername
	}
	return nil
}
