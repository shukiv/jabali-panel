package commands

// M46 Step 4 — server-wide privileged phpMyAdmin shadow (ADR-0099).
// This is, honestly, a root-equivalent web account: jabali_pma_admin
// has ALL PRIVILEGES ON *.* so the operator sees + edits every
// database incl. ones created later. The threat model is controlled
// by the gating in panel-api (RequireAdmin + same-origin + single-use
// short-TTL token + scope=admin audit), NOT by trimming this grant.
//
// Reuses the proven db_mysqladmin_ensure patterns: idempotent
// CREATE+ALTER, EscapeMariaDBLiteral, never echo mysql stderr (it can
// contain the password). The password is written to a 0640 root:jabali
// secret file (tmp+atomic rename) which panel-api's SSO validator
// reads when minting an admin-scope handoff.

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const (
	pmaAdminUser         = "jabali_pma_admin"
	pmaAdminPasswordFile = "/etc/jabali-panel/pma-admin.password"
)

type dbPmaAdminEnsureResponse struct {
	Username string `json:"username"`
}

// dbPmaAdminEnsureHandler creates/rotates jabali_pma_admin@localhost
// and writes its password to the secret file. Idempotent: safe to call
// every admin-SSO mint (cheap, rotates the password each time which is
// fine — the account is only ever used via immediate single-use SSO).
func dbPmaAdminEnsureHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	password := generateRandomPassword(32)

	escUser, err := EscapeMariaDBLiteral(pmaAdminUser)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "escape user"}
	}
	escPw, err := EscapeMariaDBLiteral(password)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "escape password"}
	}

	// Idempotent create-or-rotate + global grant (ADR-0099: ALL
	// PRIVILEGES ON *.* is inherent to "see all databases").
	sql := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS %[1]s@'localhost' IDENTIFIED BY %[2]s; "+
			"ALTER USER %[1]s@'localhost' IDENTIFIED BY %[2]s; "+
			"GRANT ALL PRIVILEGES ON *.* TO %[1]s@'localhost' WITH GRANT OPTION; "+
			"FLUSH PRIVILEGES;",
		escUser, escPw,
	)
	if err := execCommandContext(ctx, "mysql", "-e", sql).Run(); err != nil {
		// Do not echo mysql stderr — may contain the password.
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to ensure pma admin shadow"}
	}

	if err := writeSecretFile(pmaAdminPasswordFile, password); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "secret file write failed: " + err.Error()}
	}
	return dbPmaAdminEnsureResponse{Username: pmaAdminUser}, nil
}

func init() {
	Default.Register("db.pma_admin.ensure", dbPmaAdminEnsureHandler)
}
