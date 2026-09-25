// Package dbconsoleops is the DB Console SSO module (JAB-348): one issuance
// entrypoint, Issue, that every door calls — the tenant phpMyAdmin and Adminer
// handlers, the privileged admin-all handlers and `jabali db sso`.
//
// The module owns database+engine scope encoding, shadow account selection and
// provisioning, token issuance, the canonical audit outcome and redirect
// construction, so the scope is encoded identically on every door (AC3).
// Authentication, ownership, base-URL resolution and each door's audit sink and
// status codes stay adapter concerns (ADR-0083): REST resolves the user from the
// JWT, the CLI from flags.
package dbconsoleops

import (
	"context"
	"errors"
	"strings"
)

// Sentinel errors for engine normalization and shadow provisioning.
var (
	// ErrInvalidEngine means the engine string is not recognized after
	// normalization. Adapters map to their own transport: REST→400 with
	// "unknown_engine", CLI→error + exit non-zero.
	ErrInvalidEngine = errors.New("dbconsoleops: invalid engine (must be mariadb or postgres)")

	// ErrShadowProvisioning wraps agent/DB failures during shadow account
	// creation. Adapters should surface as 500/502 (REST) or fatal error (CLI).
	ErrShadowProvisioning = errors.New("dbconsoleops: shadow account provisioning failed")
)

// NormalizeEngine canonicalizes an engine string. Empty input defaults to
// "mariadb" per Jabali convention. Issue normalizes every request through it;
// the CLI also calls it for its --engine guard.
func NormalizeEngine(raw string) string {
	e := strings.TrimSpace(raw)
	if e == "" {
		return "mariadb"
	}
	return e
}

// ShadowService provisions a MariaDB account for a login: a tenant's shadow
// (sso.Service) or, for an admin-all login, the privileged account.
type ShadowService interface {
	EnsureShadow(ctx context.Context, userID string) error
}

// AdminerShadowService provisions a tenant's PostgreSQL shadow account.
// sso.AdminerService satisfies it.
type AdminerShadowService interface {
	EnsurePgShadow(ctx context.Context, userID string) error
}
