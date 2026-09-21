package api

import "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"

// ssoTokenHashPrefix returns the audit-log prefix for an SSO handoff token,
// used by BOTH the mint and validate sides so an "issued" line and its matching
// "validated"/"unauthorized" line share a value to grep for.
//
// The reduction now lives in the shared DB-console module
// (dbconsoleops.TokenAuditPrefix) so every adapter — REST here and the CLI —
// derives the prefix identically and none logs raw token material (JAB-348
// AC5). This wrapper keeps the api call sites unchanged; its test is the
// guard that the delegation does not drift from the validate-side digest.
func ssoTokenHashPrefix(token string) string {
	return dbconsoleops.TokenAuditPrefix(token)
}
