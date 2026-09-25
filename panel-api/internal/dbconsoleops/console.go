package dbconsoleops

import (
	"context"
	"errors"
)

// Issue is the DB Console SSO module's one issuance entrypoint (JAB-348
// AC1/AC2). Every door — the tenant phpMyAdmin and Adminer handlers, the
// privileged admin-all handlers and `jabali db sso` — authenticates its caller
// and resolves the database itself (ADR-0083), then calls Issue, which owns the
// rest in one order:
//
//	normalize engine → reject an unknown engine → pick the console → reject a
//	console that cannot open the engine → encode the scope → select and
//	provision the shadow account → mint the single-use token → build the
//	redirect → derive the audit hash-prefix
//
// Nothing is minted when an earlier step fails. Outcome maps the result to the
// canonical audit outcome; each door keeps its own audit sink, labels and HTTP
// status codes.

// Console is the web console a login opens.
type Console string

const (
	ConsolePhpMyAdmin Console = "phpmyadmin"
	ConsoleAdminer    Console = "adminer"
)

// Scope is what a login may reach.
type Scope string

const (
	// ScopeDatabase is one tenant database, opened with its owner's shadow
	// account.
	ScopeDatabase Scope = "database"
	// ScopeAdminAll is every database of the engine, opened with the
	// privileged account (ADR-0099). Only the admin doors may request it.
	ScopeAdminAll Scope = "admin_all"
)

// AdminAllDatabaseID is the token DatabaseID of an admin-all login. The
// validate handlers branch on it before the per-user shadow path, so it must
// never be a real database id.
const AdminAllDatabaseID = "__M46_ADMIN_ALL__"

var (
	// ErrConsoleEngine means the console cannot open a database of this
	// engine: phpMyAdmin serves MariaDB only, and an admin-all login opens
	// the engine's own console (phpMyAdmin for MariaDB, Adminer for
	// PostgreSQL).
	ErrConsoleEngine = errors.New("dbconsoleops: console cannot open this engine")
	// ErrScope means the request does not encode a valid scope: an unknown
	// scope, or a database scope without a database id or with the admin-all
	// id.
	ErrScope = errors.New("dbconsoleops: invalid console scope")
	// ErrMint means the single-use token could not be minted.
	ErrMint = errors.New("dbconsoleops: token mint failed")
	// ErrIssueDeps means a dependency the request needs is not wired — a
	// wiring bug, not a policy result.
	ErrIssueDeps = errors.New("dbconsoleops: issuance dependencies are not wired")
)

// mintError tags a minter error with ErrMint. errors.Is matches both, and
// Error() is the minter's text alone, so a door that prints the error (the CLI)
// shows what it showed before the module owned the step.
type mintError struct{ cause error }

func (e *mintError) Error() string   { return e.cause.Error() }
func (e *mintError) Unwrap() []error { return []error{ErrMint, e.cause} }

// IssueDeps are the shadow provisioners and minters an issuance may use. A door
// wires only what its requests reach; Issue checks the ones the resolved path
// needs.
type IssueDeps struct {
	// Shadow provisions a tenant's MariaDB shadow account.
	Shadow ShadowService
	// PgShadow provisions a tenant's PostgreSQL shadow account.
	PgShadow AdminerShadowService
	// PrivilegedShadow provisions the admin-all MariaDB account
	// (jabali_pma_admin). The admin-all PostgreSQL login uses the postgres
	// superuser, which the installer provisions, so it has no provisioner.
	PrivilegedShadow ShadowService
	PhpMyAdmin       PhpMyAdminMinter
	Adminer          AdminerMinter
}

// IssueRequest is one issuance after the door has authenticated the caller and,
// for ScopeDatabase, resolved the database and checked ownership.
type IssueRequest struct {
	Scope Scope
	// UserID owns the shadow account and is the token's subject (the admin,
	// for ScopeAdminAll).
	UserID string
	// DatabaseID and DBName identify the database for ScopeDatabase. They
	// are ignored for ScopeAdminAll, which always encodes AdminAllDatabaseID
	// and no database name.
	DatabaseID string
	DBName     string
	// Engine is the database's engine as stored; Issue normalizes it.
	Engine string
	// Console is the console to open. Empty picks the engine's own console:
	// phpMyAdmin for MariaDB, Adminer for PostgreSQL.
	Console Console
	// BaseURL is the console's public base URL; the door resolves it.
	BaseURL string
}

// IssueResult is the issuance's scope and, on success, its login.
type IssueResult struct {
	// Engine is the normalized engine.
	Engine string
	// Console is the console the login opens.
	Console Console
	// LoginURL is the single-use redirect. It carries the token: never log it.
	LoginURL string
	// HashPrefix identifies the token in audit lines without revealing it.
	HashPrefix string
}

// ConsoleFor is the console that opens an engine's databases by default.
func ConsoleFor(engine string) Console {
	if engine == "postgres" {
		return ConsoleAdminer
	}
	return ConsolePhpMyAdmin
}

// Issue provisions the shadow account the scope needs and mints a single-use
// console login. On any error nothing was minted, LoginURL and HashPrefix are
// empty, and Engine/Console are set as far as they were resolved, for the
// door's audit line.
func Issue(ctx context.Context, d IssueDeps, req IssueRequest) (IssueResult, error) {
	res := IssueResult{Engine: NormalizeEngine(req.Engine)}
	if res.Engine != "mariadb" && res.Engine != "postgres" {
		return res, ErrInvalidEngine
	}

	res.Console = req.Console
	if res.Console == "" {
		res.Console = ConsoleFor(res.Engine)
	}
	switch res.Console {
	case ConsolePhpMyAdmin:
		if res.Engine != "mariadb" {
			return res, ErrConsoleEngine
		}
	case ConsoleAdminer:
	default:
		return res, ErrConsoleEngine
	}

	databaseID, dbName := req.DatabaseID, req.DBName
	switch req.Scope {
	case ScopeDatabase:
		if databaseID == "" || databaseID == AdminAllDatabaseID {
			return res, ErrScope
		}
	case ScopeAdminAll:
		if res.Console != ConsoleFor(res.Engine) {
			return res, ErrConsoleEngine
		}
		databaseID, dbName = AdminAllDatabaseID, ""
	default:
		return res, ErrScope
	}

	// Every dependency the resolved path reaches is checked before the first
	// side effect, so a wiring bug never provisions an account it cannot use.
	provision := shadowFor(d, req.Scope, res.Engine)
	if provision == nil ||
		(res.Console == ConsolePhpMyAdmin && d.PhpMyAdmin == nil) ||
		(res.Console == ConsoleAdminer && d.Adminer == nil) {
		return res, ErrIssueDeps
	}
	if err := provision(ctx, req.UserID); err != nil {
		return res, errors.Join(ErrShadowProvisioning, err)
	}

	var err error
	if res.Console == ConsolePhpMyAdmin {
		res.LoginURL, res.HashPrefix, err = IssuePhpMyAdminLogin(ctx, d.PhpMyAdmin, req.UserID, databaseID, dbName, req.BaseURL)
	} else {
		res.LoginURL, res.HashPrefix, err = IssueAdminerLogin(ctx, d.Adminer, req.UserID, databaseID, dbName, res.Engine, req.BaseURL)
	}
	if err != nil {
		res.LoginURL, res.HashPrefix = "", ""
		return res, &mintError{cause: err}
	}
	return res, nil
}

// shadowFor selects the account a login of this scope and engine signs in as,
// and returns the step that provisions it — nil when that provisioner is not
// wired:
//
//	database  + mariadb  → the owner's MariaDB shadow (Shadow)
//	database  + postgres → the owner's PostgreSQL shadow (PgShadow)
//	admin_all + mariadb  → jabali_pma_admin (PrivilegedShadow)
//	admin_all + postgres → the postgres superuser, which the installer
//	                       provisions: nothing to do
func shadowFor(d IssueDeps, scope Scope, engine string) func(context.Context, string) error {
	switch {
	case scope == ScopeAdminAll && engine == "postgres":
		return func(context.Context, string) error { return nil }
	case scope == ScopeAdminAll:
		if d.PrivilegedShadow == nil {
			return nil
		}
		return d.PrivilegedShadow.EnsureShadow
	case engine == "postgres":
		if d.PgShadow == nil {
			return nil
		}
		return d.PgShadow.EnsurePgShadow
	default:
		if d.Shadow == nil {
			return nil
		}
		return d.Shadow.EnsureShadow
	}
}

// Outcome is the canonical audit outcome of an Issue call: OutcomeIssued,
// OutcomeEnsureShadowFail or OutcomeMintFail. A rejection raised before
// issuance starts (ErrInvalidEngine, ErrConsoleEngine, ErrScope) or a wiring
// error returns "": each door labels those with its own taxonomy.
func Outcome(err error) string {
	switch {
	case err == nil:
		return OutcomeIssued
	case errors.Is(err, ErrShadowProvisioning):
		return OutcomeEnsureShadowFail
	case errors.Is(err, ErrMint):
		return OutcomeMintFail
	default:
		return ""
	}
}
