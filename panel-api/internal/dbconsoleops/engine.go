// Package dbconsoleops provides transport-neutral policy for DB Console SSO.
//
// Scope encoding (engine normalization and dispatch) is the deep module that
// both REST handlers and the CLI adapter call, ensuring database+engine scope
// is encoded identically across every path (AC3 of JAB-348).
//
// Authorization/identity resolution stay adapter concerns (per ADR-0083).
// REST resolves user from JWT, CLI resolves from flags. This leaf only decides
// which shadow path to provision and validates engine parameter.
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
	// "invalid_engine", CLI→error + exit non-zero.
	ErrInvalidEngine = errors.New("dbconsoleops: invalid engine (must be mariadb or postgres)")

	// ErrShadowProvisioning wraps agent/DB failures during shadow account
	// creation. Adapters should surface as 500/502 (REST) or fatal error (CLI).
	ErrShadowProvisioning = errors.New("dbconsoleops: shadow account provisioning failed")
)

// NormalizeEngine canonicalizes an engine string. Empty input defaults to
// "mariadb" per Jabali convention. Returns one of {"mariadb", "postgres"}.
// This is the single place engine defaults are set — both CLI (db_sso_cmd.go)
// and REST adminer handler (sso_adminer.go) previously had identical copies.
//
// Post-normalization, the engine is valid for all downstream ops:
//   - EnsureShadowForEngine
//   - sso.AdminerService.MintAdminerToken
//   - url parameter encoding
func NormalizeEngine(raw string) string {
	e := strings.TrimSpace(raw)
	if e == "" {
		return "mariadb"
	}
	return e
}

// ShadowService describes the minimal interface the sso package exports for
// shadow account provisioning. Both sso.Service (phpMyAdmin) and
// sso.AdminerService (Adminer) satisfy this.
type ShadowService interface {
	EnsureShadow(ctx context.Context, userID string) error
}

// AdminerShadowService describes the additional interface for Adminer's
// PostgreSQL shadow provisioning. sso.AdminerService satisfies this.
type AdminerShadowService interface {
	EnsurePgShadow(ctx context.Context, userID string) error
}

// EnsureShadowForEngine dispatches to the correct shadow provisioner based on
// engine. The first two parameters are the unified interface and the
// engine-specific service. Adapters pass:
//
//   base: sso.Service (for both mariadb and postgres)
//   adminer: sso.AdminerService (for postgres specialization)
//   engine: result of NormalizeEngine(...)
//   userID: already-resolved user from JWT (REST) or CLI args
//
// This consolidates the identical engine switch statements that were duplicated
// in db_sso_cmd.go (lines 68-95) and sso_adminer.go (lines 93-112).
//
// Errors:
//   - ErrInvalidEngine: engine is not "mariadb" or "postgres"
//   - ErrShadowProvisioning: wrapped error from the agent/DB call
//     (EnsureShadow/EnsurePgShadow). Caller logs the detail and surfaces as
//     5xx or fatal error to the user.
func EnsureShadowForEngine(
	ctx context.Context,
	engine string,
	userID string,
	base ShadowService,
	adminer AdminerShadowService,
) error {
	switch engine {
	case "mariadb":
		if err := base.EnsureShadow(ctx, userID); err != nil {
			return errors.Join(ErrShadowProvisioning, err)
		}
		return nil

	case "postgres":
		if err := adminer.EnsurePgShadow(ctx, userID); err != nil {
			return errors.Join(ErrShadowProvisioning, err)
		}
		return nil

	default:
		return ErrInvalidEngine
	}
}
