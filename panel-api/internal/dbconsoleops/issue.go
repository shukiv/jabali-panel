package dbconsoleops

import "context"

// Console issuance tail — "mint a single-use token, derive its audit
// hash-prefix, and build the matching redirect URL" (JAB-348). Issue
// (console.go) is the only caller: it runs these leaves after it has selected
// and provisioned the shadow account, so the token minter and the redirect
// builder can never be paired wrongly — an Adminer token always gets an Adminer
// redirect, a phpMyAdmin token always gets a phpMyAdmin redirect. Doors call
// Issue, never these leaves.

// OutcomeIssued / OutcomeMintFail / OutcomeEnsureShadowFail are the canonical
// audit-outcome strings for a DB-console issuance. They give every adapter one
// taxonomy to draw from for the issuance path instead of hand-typed literals
// (JAB-348 AC1/AC5). The values are exactly the strings the Adminer and CLI doors
// already emit, so adopting the constants changes no audit output.
const (
	OutcomeIssued           = "issued"
	OutcomeMintFail         = "mint_fail"
	OutcomeEnsureShadowFail = "ensure_shadow_fail"
)

// PhpMyAdminMinter is the minimal mint surface the phpMyAdmin console leaf needs.
// *sso.Service satisfies it.
type PhpMyAdminMinter interface {
	MintToken(ctx context.Context, userID, databaseID, dbName string) (string, error)
}

// AdminerMinter is the minimal mint surface the Adminer console leaf needs.
// *sso.AdminerService satisfies it.
type AdminerMinter interface {
	MintAdminerToken(ctx context.Context, userID, databaseID, engine string) (string, error)
}

// PhpMyAdminConsole is the full shadow + mint surface the tenant phpMyAdmin door
// depends on. *sso.Service satisfies it. Typing the handler config field as this
// interface (rather than the concrete *sso.Service) lets a single fake drive the
// phpMyAdmin door through the DB-Console SSO contract matrix — the same injection
// seam the privileged doors already expose — so tenant, privileged, and CLI
// adapters can be exercised against one request matrix (JAB-348 AC4).
type PhpMyAdminConsole interface {
	ShadowService
	PhpMyAdminMinter
}

// AdminerConsole is the full postgres-shadow + mint surface the tenant Adminer
// door's Adminer dependency provides. *sso.AdminerService satisfies it. Typed on
// the handler config for the same AC4 reason as PhpMyAdminConsole. (The Adminer
// door's mariadb shadow provisioning goes through the separate ShadowService
// dependency — IssueDeps.Shadow beside IssueDeps.PgShadow.)
type AdminerConsole interface {
	AdminerShadowService
	AdminerMinter
}

// IssuePhpMyAdminLogin mints a single-use phpMyAdmin token for (userID,
// databaseID) and returns the phpMyAdmin login URL under baseURL plus the audit
// hash-prefix for that token. dbName is the scope label carried in the redirect
// (empty for the admin-all door, which mints against a sentinel databaseID). On a
// mint failure the underlying sso error is returned and loginURL/hashPrefix are
// empty (the caller maps that to OutcomeMintFail).
func IssuePhpMyAdminLogin(
	ctx context.Context,
	m PhpMyAdminMinter,
	userID, databaseID, dbName, baseURL string,
) (loginURL, hashPrefix string, err error) {
	token, err := m.MintToken(ctx, userID, databaseID, dbName)
	if err != nil {
		return "", "", err
	}
	return PhpMyAdminRedirect(baseURL, token, dbName), TokenAuditPrefix(token), nil
}

// IssueAdminerLogin mints a single-use Adminer token for (userID, databaseID) on
// the given engine and returns the Adminer login URL under baseURL plus the audit
// hash-prefix. On a mint failure the underlying sso error is returned and
// loginURL/hashPrefix are empty (the caller maps that to OutcomeMintFail).
func IssueAdminerLogin(
	ctx context.Context,
	m AdminerMinter,
	userID, databaseID, dbName, engine, baseURL string,
) (loginURL, hashPrefix string, err error) {
	token, err := m.MintAdminerToken(ctx, userID, databaseID, engine)
	if err != nil {
		return "", "", err
	}
	return AdminerRedirect(baseURL, token, dbName, engine), TokenAuditPrefix(token), nil
}
