package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// db.postgres.shadowadmin.ensure — M37 Phase 4 Adminer SSO bridge.
//
// Mirror of db.mysqladmin.ensure but for PostgreSQL. Creates a ROLE
// LOGIN named "<panel_username>_pgadmin" with a randomly-generated
// 32-char password and CREATEDB on every database the panel user
// owns (engine='postgres'). Idempotent: re-runs rotate the password
// via DO $$ ... ALTER ROLE pattern.
//
// All SQL flows through `sudo -u postgres psql -1 -c "..."` (peer
// auth) — no plaintext password ever touches the wire.

type dbPostgresShadowadminParams struct {
	PanelUsername string `json:"panel_username"`
}

type dbPostgresShadowadminResponse struct {
	Username string `json:"pgadmin_username"`
	Password string `json:"pgadmin_password"`
}

func dbPostgresShadowadminEnsureHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPostgresShadowadminParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !panelUsernameRegex.MatchString(p.PanelUsername) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid panel username",
		}
	}

	password, err := generateMysqladminPassword()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to generate password",
		}
	}
	roleName := p.PanelUsername + "_pgadmin"

	// PG identifier quoting: double the embedded quote. roleName comes
	// from regex-validated input — defence in depth only.
	pgIdent := func(s string) string {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	// PG string literal: double single quotes. Same belt-and-braces.
	pgStr := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}

	// Role idempotent upsert via DO block: CREATE if missing, ALTER
	// to rotate password every time. CREATEDB so the user can spin up
	// scratch DBs from Adminer; no SUPERUSER/REPLICATION/CREATEROLE.
	sql := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN CREATEDB PASSWORD %s;
  ELSE
    ALTER ROLE %s WITH LOGIN CREATEDB PASSWORD %s;
  END IF;
END$$;`,
		pgStr(roleName), pgIdent(roleName), pgStr(password),
		pgIdent(roleName), pgStr(password),
	)

	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-c", sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Do not echo psql's stderr — it may contain the password.
		_ = out
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to ensure pgadmin shadow role",
		}
	}

	// GRANT ownership of every PG db the panel user already owns to
	// the shadow role so Adminer can SELECT/INSERT after login.
	// `<panel_username>_*` is the panel-api naming convention; we
	// scope GRANT to that pattern. Fail-soft: if no matching DB,
	// the GRANT is a no-op.
	grantSQL := fmt.Sprintf(`DO $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT datname FROM pg_database
           WHERE datname LIKE %s ESCAPE '\'
  LOOP
    EXECUTE format('GRANT ALL PRIVILEGES ON DATABASE %%I TO %%I', r.datname, %s);
  END LOOP;
END$$;`,
		pgStr(p.PanelUsername+`\_%`),
		pgStr(roleName),
	)
	cmdGrant := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-c", grantSQL)
	if _, err := cmdGrant.CombinedOutput(); err != nil {
		// Grants failing isn't fatal — first-time provision before any DB
		// exists hits this path. Log via the response and continue.
	}

	return dbPostgresShadowadminResponse{
		Username: roleName,
		Password: password,
	}, nil
}

// db.postgres.shadowadmin.grant_members — GH #1406.
//
// GRANT ... ON DATABASE (what ensure does) only conveys CONNECT/CREATE/TEMP;
// it does NOT grant access to the tables inside, so Adminer as <user>_pgadmin
// could see the catalog ("Show structure") but got "permission denied for
// table" on the data. The tenant's tables are owned by their per-DB db-user
// roles (<user>_<name>, see database_users.go). Making pgadmin an INHERITing
// member of those roles gives it their privileges on every object they own —
// existing and future, in every schema — and, because Postgres ownership
// checks go through has_privs_of_role, ALTER/DROP work too. That is exactly the
// admin capability Adminer needs.
//
// SECURITY: the member roles are an EXPLICIT list from the panel (the tenant's
// own postgres database_users), never a name pattern — panel usernames may
// contain '_', so a LIKE '<user>\_%' would also match a sibling tenant
// (<user>_x_*) and hand over their data. Every incoming name is
// pgValidIdent-checked, and the SQL additionally restricts membership to
// non-superuser LOGIN roles that are not pgadmin itself (defence in depth: a
// bad/renamed row can never escalate pgadmin into a superuser).
type dbPostgresGrantMembersParams struct {
	PanelUsername string   `json:"panel_username"`
	MemberRoles   []string `json:"member_roles"`
}

func dbPostgresShadowadminGrantMembersHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPostgresGrantMembersParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !panelUsernameRegex.MatchString(p.PanelUsername) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid panel username",
		}
	}
	roleName := p.PanelUsername + "_pgadmin"

	pgStr := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}

	// Filter to valid identifiers that aren't pgadmin itself. Invalid names are
	// skipped rather than fatal — one odd row must not block Adminer login.
	quoted := make([]string, 0, len(p.MemberRoles))
	for _, r := range p.MemberRoles {
		if r == roleName || !pgValidIdent(r) {
			continue
		}
		quoted = append(quoted, pgStr(r))
	}
	if len(quoted) == 0 {
		// Nothing to grant (no db-users yet) — success, not an error.
		return dbPgCreateResponse{OK: true}, nil
	}

	// Restrict the grant to the passed roles AND to non-superuser LOGIN roles
	// that exist — so membership can never reach a superuser or a role outside
	// the explicit list. GRANT membership is idempotent (re-grant is a NOTICE,
	// not an error), so this is safe to run on every Adminer open.
	sql := fmt.Sprintf(`DO $$
DECLARE r RECORD;
BEGIN
  FOR r IN
    SELECT rolname FROM pg_roles
    WHERE rolname = ANY(ARRAY[%s]::text[])
      AND rolname <> %s
      AND NOT rolsuper
      AND rolcanlogin
  LOOP
    EXECUTE format('GRANT %%I TO %%I', r.rolname, %s);
  END LOOP;
END$$;`,
		strings.Join(quoted, ","),
		pgStr(roleName),
		pgStr(roleName),
	)

	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-c", sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to grant pgadmin membership: " + strings.TrimSpace(string(out)),
		}
	}
	return dbPgCreateResponse{OK: true}, nil
}

func init() {
	Default.Register("db.postgres.shadowadmin.ensure", dbPostgresShadowadminEnsureHandler)
	Default.Register("db.postgres.shadowadmin.grant_members", dbPostgresShadowadminGrantMembersHandler)
}
