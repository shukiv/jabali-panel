// Package logaccess holds the authorization policy shared by every adapter that
// mints a log-stream access grant (the HTTP /logs handler and the operator
// `jabali log access create` CLI).
//
// Security (JAB-303): a log-stream grant with a nil DomainID streams SYSTEM-WIDE
// logs — nginx access/error and the GoAccess report for the whole server — as
// resolved by the WS stream handler (websocket_logs.go: `stream.DomainID == nil`
// takes the systemLogPath branch). The grant record is the ONLY gate; the WS
// consumer authorizes purely on the stream key + the grant's scope. Therefore
// the moment of minting is where the actor/beneficiary scope must be enforced.
//
// The HTTP adapter already enforced this (a non-admin caller must name a domain
// they own); the CLI adapter did not, so an operator could mint a nil-domain,
// hence server-wide, grant OWNED BY A TENANT — a cross-tenant / system log
// disclosure. Both adapters now route the decision through ValidateGrantScope so
// the policy cannot drift between them again.
package logaccess

import "errors"

// MaxActiveStreamsPerUser is the per-user cap on concurrent active log-stream
// grants, enforced by both adapters through
// LogAccessStreamRepository.ReserveWithinCap (JAB-347). Shared here so the HTTP
// and CLI adapters cannot drift, matching the ValidateGrantScope pattern.
const MaxActiveStreamsPerUser = 5

// ErrDomainRequired is returned when a non-admin beneficiary omits the domain.
// A nil-domain grant is server-wide, so it is never valid for a tenant.
var ErrDomainRequired = errors.New("log access: a domain owned by the beneficiary is required for non-admin grants")

// ErrDomainNotOwned is returned when a non-admin beneficiary names a domain that
// they do not own.
var ErrDomainNotOwned = errors.New("log access: domain is not owned by the grant beneficiary")

// ValidateGrantScope enforces the log-stream grant scope policy.
//
//   - beneficiaryIsAdmin — admin status of the grant OWNER (the user the stream
//     is minted for), NOT necessarily the caller. In the HTTP path the caller is
//     the beneficiary; in the CLI path an operator mints for --user.
//   - domainProvided — whether the grant is scoped to a specific domain.
//   - domainOwnedByBeneficiary — whether that domain belongs to the beneficiary
//     (ignored when domainProvided is false).
//
// Rules:
//   - No domain: allowed only for an admin beneficiary (server-wide scope).
//     A non-admin beneficiary is rejected with ErrDomainRequired.
//   - Domain given: a non-admin beneficiary must own it, else ErrDomainNotOwned.
//     An admin beneficiary may be scoped to any domain (an admin already has
//     server-wide access, so a single tenant's domain is strictly narrower).
func ValidateGrantScope(beneficiaryIsAdmin, domainProvided, domainOwnedByBeneficiary bool) error {
	if !domainProvided {
		if !beneficiaryIsAdmin {
			return ErrDomainRequired
		}
		return nil
	}
	if !beneficiaryIsAdmin && !domainOwnedByBeneficiary {
		return ErrDomainNotOwned
	}
	return nil
}
