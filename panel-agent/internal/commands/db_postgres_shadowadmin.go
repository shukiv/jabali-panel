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
// LOGIN CREATEDB named "<panel_username>_pgadmin" with a randomly-generated
// 32-char password. Idempotent: re-runs rotate the password via the
// DO $$ ... ALTER ROLE pattern. It grants no database: grant_schema grants
// the tenant's own databases by exact name on every Adminer open.
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

	// No database grant here. This used to GRANT ALL ON DATABASE to every
	// database matching datname LIKE '<panel_username>\_%', which also matched
	// a sibling tenant's databases (panel usernames may contain '_').
	// grant_schema now grants the tenant's own databases from the explicit
	// control-plane list and revokes any other database grant.

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

// db.postgres.shadowadmin.grant_schema — GH #1406 (round 2).
//
// Membership (grant_members) lets pgadmin INHERIT privileges on objects the
// tenant's db-user roles OWN. But a Jabali PostgreSQL database is created
// OWNER postgres (dbops.go), so its `public` schema is owned by
// pg_database_owner = postgres, and — since PostgreSQL 15 revoked the default
// PUBLIC CREATE on `public` — a tenant role can neither CREATE in `public`
// ("permission denied for schema public") nor SELECT postgres-owned tables in
// it ("permission denied for table"). Reproduced on PG16 + PG18.
//
// So on every Adminer open, grant <user>_pgadmin the schema-level privileges it
// needs, PER DATABASE (schema grants only affect the connected DB, so this
// connects to each): ALL ON SCHEMA public (CREATE+USAGE), ALL on existing
// TABLES/SEQUENCES (covers postgres-owned + restored objects that membership
// can't reach), and default privileges so future objects stay accessible.
//
// It also owns pgadmin's DATABASE-level privileges (CONNECT/CREATE/TEMP):
// GRANT ALL ON DATABASE for each listed database, and REVOKE ALL ON DATABASE
// on every other database where pgadmin holds an explicit grant, except the
// scratch databases pgadmin itself owns (CREATEDB). The revoke removes the
// grants the old ensure handed out by name pattern on existing boxes. An empty
// list still runs the revoke.
//
// SECURITY: db_names is an EXPLICIT list of the tenant's OWN databases from the
// control-plane (scoped by user_id), never a name pattern — a `datname LIKE
// '<user>\_%'` would also match a sibling tenant's DB (panel usernames allow
// '_') and hand pgadmin full table access to another tenant's data. Every name
// is pgValidIdent-checked. The per-DB schema grants are fail-soft; a failure of
// the database-level grant/revoke fails the call so the panel refuses the
// Adminer session instead of opening it with a grant that may reach another
// tenant.
type dbPostgresGrantSchemaParams struct {
	PanelUsername string   `json:"panel_username"`
	DBNames       []string `json:"db_names"`
}

func dbPostgresShadowadminGrantSchemaHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPostgresGrantSchemaParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if !panelUsernameRegex.MatchString(p.PanelUsername) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid panel username"}
	}
	roleName := p.PanelUsername + "_pgadmin"
	pgIdent := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

	// Grant statements run against EACH DB (schema/object grants are per-DB).
	// pgadmin is validated; the DB name is validated + passed via -d, never
	// interpolated into SQL.
	roleQ := pgIdent(roleName)
	pgStr := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

	dbs := make([]string, 0, len(p.DBNames))
	quotedDBs := make([]string, 0, len(p.DBNames))
	for _, db := range p.DBNames {
		if !pgValidIdent(db) {
			continue // never grant an unexpected name; the revoke still runs
		}
		dbs = append(dbs, db)
		quotedDBs = append(quotedDBs, pgStr(db))
	}

	// Database-level privileges, from the maintenance DB: grant the listed
	// databases, then revoke every other explicit grant pgadmin holds on a
	// database it does not own.
	dbLevelSQL := fmt.Sprintf(`DO $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT datname FROM pg_database
           WHERE datname = ANY(ARRAY[%[1]s]::text[])
  LOOP
    EXECUTE format('GRANT ALL PRIVILEGES ON DATABASE %%I TO %%I', r.datname, %[2]s);
  END LOOP;
  FOR r IN SELECT DISTINCT d.datname
           FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a
           WHERE d.datacl IS NOT NULL
             AND a.grantee = (SELECT oid FROM pg_roles WHERE rolname = %[2]s)
             AND d.datdba <> a.grantee
             AND NOT (d.datname = ANY(ARRAY[%[1]s]::text[]))
  LOOP
    EXECUTE format('REVOKE ALL PRIVILEGES ON DATABASE %%I FROM %%I', r.datname, %[2]s);
  END LOOP;
END$$;`,
		strings.Join(quotedDBs, ","),
		pgStr(roleName),
	)
	dbLevel := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-c", dbLevelSQL)
	if out, err := dbLevel.CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to sync pgadmin database grants: " + strings.TrimSpace(string(out)),
		}
	}

	grantSQL := strings.Join([]string{
		"GRANT ALL ON SCHEMA public TO " + roleQ,
		"GRANT ALL ON ALL TABLES IN SCHEMA public TO " + roleQ,
		"GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO " + roleQ,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO " + roleQ,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO " + roleQ,
	}, "; ") + ";"

	for _, db := range dbs {
		// -d <db> selects the target database; run as postgres (peer auth).
		cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
			"-v", "ON_ERROR_STOP=1", "-XAtq", "-d", db, "-c", grantSQL)
		if _, err := cmd.CombinedOutput(); err != nil {
			// Fail-soft: a dropped/renamed DB or a transient error must not block
			// the Adminer session or the other DBs.
			continue
		}
	}
	return dbPgCreateResponse{OK: true}, nil
}

func init() {
	Default.Register("db.postgres.shadowadmin.ensure", dbPostgresShadowadminEnsureHandler)
	Default.Register("db.postgres.shadowadmin.grant_members", dbPostgresShadowadminGrantMembersHandler)
	Default.Register("db.postgres.shadowadmin.grant_schema", dbPostgresShadowadminGrantSchemaHandler)
}
