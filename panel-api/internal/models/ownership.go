package models

import "time"

// Ownership proof (GH #1816 / ADR-0170). A domain, or a web alias outside a
// verified domain of the same owner, stays pending until the owner proves
// control of the name or an administrator approves it.

// Ownership statuses. Any value other than OwnershipVerified is treated as
// pending, so an unknown or empty status never goes live.
const (
	OwnershipPending  = "pending"
	OwnershipVerified = "verified"
)

// Ownership methods: how a row became verified.
const (
	// OwnershipMethodDNSTXT: the owner published the challenge TXT record.
	OwnershipMethodDNSTXT = "dns_txt"
	// OwnershipMethodAdmin: an administrator created or approved the name.
	OwnershipMethodAdmin = "admin"
	// OwnershipMethodParent: the nearest hosted ancestor is verified and is
	// owned by the same user or delegates its subdomains (GH #1812).
	OwnershipMethodParent = "parent"
	// OwnershipMethodLegacy: the row existed before migration 000311.
	OwnershipMethodLegacy = "legacy"
	// OwnershipMethodMigration: an administrator pulled it from another server.
	OwnershipMethodMigration = "migration"
	// OwnershipMethodRestore: an administrator restored it from a backup.
	OwnershipMethodRestore = "restore"
	// OwnershipMethodAutomation: a billing system vouched for it through an
	// automation token that holds the assert:domain_ownership scope.
	OwnershipMethodAutomation = "automation"
	// OwnershipMethodPolicyOff: created while the administrator had turned
	// the proof requirement off. Such a row stays verified if the policy is
	// turned on again later.
	OwnershipMethodPolicyOff = "policy_off"
)

// Ownership check results, stored in ownership_last_result and shown to the
// owner.
const (
	// OwnershipResultVerified: two or more public resolvers returned the
	// challenge value.
	OwnershipResultVerified = "verified"
	// OwnershipResultNotFound: the challenge record does not exist.
	OwnershipResultNotFound = "not_found"
	// OwnershipResultMismatch: the challenge name has TXT records, but none
	// holds this domain's value.
	OwnershipResultMismatch = "mismatch"
	// OwnershipResultPropagating: only one resolver returns the value yet.
	OwnershipResultPropagating = "propagating"
	// OwnershipResultNSPointsHere: the name's public nameservers are this
	// server's own, so no DNS proof can pass. An administrator must approve.
	OwnershipResultNSPointsHere = "ns_points_here"
	// OwnershipResultDNSUnresolvable: public resolvers are reachable but
	// cannot resolve the name (often: its nameservers already point here).
	OwnershipResultDNSUnresolvable = "dns_unresolvable"
	// OwnershipResultResolversUnreachable: this server could not reach the
	// public resolvers. Nothing is known about the name.
	OwnershipResultResolversUnreachable = "resolvers_unreachable"
)

// OwnershipChallengePrefix is the label the challenge TXT record sits under:
// _jabali-challenge.<name>.
const OwnershipChallengePrefix = "_jabali-challenge."

// OwnershipValuePrefix starts the challenge TXT value: jabali-verify=<token>.
const OwnershipValuePrefix = "jabali-verify="

// OwnershipState is the ownership-proof state a domain or a web alias carries
// (migration 000311). The status column defaults to 'pending' in the database
// and in GORM, so an insert that leaves OwnershipStatus empty fails closed.
//
// Every write goes through the dedicated repository setters. The domain
// repository's generic Update is a Select allow-list that does not name these
// columns, so it can never change them.
type OwnershipState struct {
	OwnershipStatus string `gorm:"column:ownership_status;type:varchar(16);not null;default:pending" json:"ownership_status"`
	OwnershipMethod string `gorm:"column:ownership_method;type:varchar(16);not null;default:''" json:"ownership_method"`
	// OwnershipToken is the challenge value's random part. It is public once
	// the owner publishes it, and it is shown only to the owner and admins.
	OwnershipToken            string     `gorm:"column:ownership_token;type:varchar(64);not null;default:''" json:"ownership_token"`
	OwnershipPendingSince     *time.Time `gorm:"column:ownership_pending_since;type:datetime(6)" json:"ownership_pending_since,omitempty"`
	OwnershipVerifiedAt       *time.Time `gorm:"column:ownership_verified_at;type:datetime(6)" json:"ownership_verified_at,omitempty"`
	OwnershipCheckedAt        *time.Time `gorm:"column:ownership_checked_at;type:datetime(6)" json:"ownership_checked_at,omitempty"`
	OwnershipNextCheckAt      *time.Time `gorm:"column:ownership_next_check_at;type:datetime(6)" json:"ownership_next_check_at,omitempty"`
	OwnershipLastResult       string     `gorm:"column:ownership_last_result;type:varchar(32);not null;default:''" json:"ownership_last_result"`
	OwnershipExpiryNotifiedAt *time.Time `gorm:"column:ownership_expiry_notified_at;type:datetime(6)" json:"ownership_expiry_notified_at,omitempty"`
}

// Verified reports whether the owner's control of the name is proven. It is
// the one predicate every gate uses: only the exact 'verified' status counts.
func (s OwnershipState) Verified() bool { return s.OwnershipStatus == OwnershipVerified }

// DomainOwnershipSettings is the singleton policy row (id=1). A missing row
// means proof is required.
type DomainOwnershipSettings struct {
	ID           uint8     `gorm:"column:id;primaryKey" json:"-"`
	RequireProof bool      `gorm:"column:require_proof;type:tinyint(1);not null" json:"require_proof"`
	UpdatedBy    string    `gorm:"column:updated_by;type:varchar(64);not null;default:''" json:"updated_by"`
	UpdatedAt    time.Time `gorm:"column:updated_at;type:datetime(3);not null" json:"updated_at"`
}

func (DomainOwnershipSettings) TableName() string { return "domain_ownership_settings" }
