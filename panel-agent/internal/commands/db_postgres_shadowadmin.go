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

func init() {
	Default.Register("db.postgres.shadowadmin.ensure", dbPostgresShadowadminEnsureHandler)
}
