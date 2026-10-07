package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// db.postgres.* commands — M37 Wave A.
//
// Authn path: peer auth on /run/postgresql/.s.PGSQL.5432. The agent
// runs as root; we shell out as the `postgres` system user via
// `sudo -u postgres psql` so the local socket connection authenticates
// as the postgres superuser without password handling here.
//
// Identifier validation mirrors db_create.go's MariaDB pattern: letters,
// digits, underscores, hyphens; first char a letter; max 63 chars
// (Postgres NAMEDATALEN-1 default).

var pgIdentRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`)

func pgValidIdent(s string) bool {
	if !pgIdentRegex.MatchString(s) {
		return false
	}
	for _, c := range s {
		if c == '\'' || c == '"' || c == ';' || c == '\n' || c == '\r' || c == ' ' || c == '\\' {
			return false
		}
	}
	return true
}

// pgRunSQL shells out to `sudo -u postgres psql -c <sql>`. Identifiers
// in the SQL must already be validated via pgValidIdent — we don't
// prepared-statement here because role / database names are
// identifiers, not values, and PG doesn't support parameterised DDL.
func pgRunSQL(ctx context.Context, sql string) error {
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1",
		"-XAtq",
		"-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql: %w (stderr/stdout: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- db.postgres.create_db ----

type dbPgCreateParams struct {
	DBName string `json:"db_name"`
	Owner  string `json:"owner"`
}

type dbPgCreateResponse struct {
	OK bool `json:"ok"`
}

func dbPgCreateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.DBName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid database name"}
	}
	owner := p.Owner
	if owner == "" {
		owner = "postgres"
	} else if !pgValidIdent(owner) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid owner"}
	}

	sql := fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s" ENCODING 'UTF8' TEMPLATE template0`, p.DBName, owner)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "create db: " + err.Error()}
	}
	if err := pgRunSQL(ctx, pgRevokePublicSQL(p.DBName)); err != nil {
		// Never hand out a database every role can connect to.
		_ = pgRunSQL(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, p.DBName))
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "create db: revoke public access: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// pgRevokePublicSQL takes CONNECT and TEMPORARY on a database away from
// PUBLIC. Postgres grants both to every role on a new database, so any
// tenant's role could connect to any other tenant's database, read its
// catalog (table and column names, view and function source) and create
// temporary tables there. The owner, and roles granted on the database, keep
// their access. db must already be validated with pgValidIdent.
func pgRevokePublicSQL(db string) string {
	return fmt.Sprintf(`REVOKE CONNECT, TEMPORARY ON DATABASE "%s" FROM PUBLIC`, db)
}

// ---- db.postgres.drop_db ----

type dbPgDropParams struct {
	DBName string `json:"db_name"`
}

func dbPgDropHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgDropParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.DBName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid database name"}
	}
	sql := fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, p.DBName)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "drop db: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// ---- db.postgres.create_role ----

type dbPgCreateRoleParams struct {
	Role     string `json:"role"`
	Password string `json:"password"`
	// PasswordVerifier gives the role a SCRAM-SHA-256 verifier instead of a
	// password: a restored role's, from its backup (GH #1993). Without
	// CreateOnly it sets an existing role's password, which must exist.
	PasswordVerifier string `json:"password_verifier"`
	// CreateOnly never changes an existing role: a role with this name is
	// not the caller's, whoever holds it, and the answer is already_exists.
	CreateOnly bool `json:"create_only"`
}

func dbPgCreateRoleHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgCreateRoleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.Role) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid role name"}
	}
	if p.PasswordVerifier != "" {
		if !backup.IsPostgresSCRAMVerifier(p.PasswordVerifier) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid password verifier (not SCRAM-SHA-256)"}
		}
		return pgSetRoleSecret(ctx, p.Role, p.PasswordVerifier, p.CreateOnly)
	}
	if p.Password == "" || strings.ContainsAny(p.Password, "'\\\n\r") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid password (empty or contains forbidden chars)"}
	}
	if p.CreateOnly {
		return pgSetRoleSecret(ctx, p.Role, p.Password, true)
	}
	// CREATE ROLE / IF NOT EXISTS via DO block — PG lacks IF NOT EXISTS
	// on CREATE ROLE pre-9.x; modern still doesn't have it on the
	// CREATE ROLE statement directly.
	sql := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
    CREATE ROLE "%s" WITH LOGIN PASSWORD '%s';
  ELSE
    ALTER ROLE "%s" WITH LOGIN PASSWORD '%s';
  END IF;
END $$;`, p.Role, p.Role, p.Password, p.Role, p.Password)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "create role: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// Markers pgSetRoleSecret's script prints when the role is, or isn't, there,
// or is one it never changes.
const (
	pgRoleExistsMarker     = "JABALI_ROLE_EXISTS"
	pgRoleMissingMarker    = "JABALI_ROLE_MISSING"
	pgRolePrivilegedMarker = "JABALI_ROLE_PRIVILEGED"
)

// pgSetRoleSecret gives role the secret: a password, or a SCRAM-SHA-256
// verifier, which PostgreSQL stores as it is. With createOnly it creates the
// role and never changes an existing one (already_exists); without, it sets
// an existing role's password (not_found when there is none), except a
// superuser's or another role with server-wide rights (permission_denied):
// those are never a tenant's. The script reaches psql on stdin and the
// statements take the values as psql variables, so no SQL is built from
// them. role passed pgValidIdent and the secret has no quote, backslash or
// line break, so \set takes both as they are.
func pgSetRoleSecret(ctx context.Context, role, secret string, createOnly bool) (any, error) {
	var b strings.Builder
	b.WriteString("\\set role '" + role + "'\n")
	b.WriteString("\\set secret '" + secret + "'\n")
	b.WriteString("SELECT (count(*) > 0)::int AS role_exists," +
		" (count(*) FILTER (WHERE rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls) > 0)::int AS role_privileged" +
		" FROM pg_roles WHERE rolname = :'role' \\gset\n")
	if createOnly {
		b.WriteString("\\if :role_exists\n\\echo " + pgRoleExistsMarker + "\n\\else\nCREATE ROLE :\"role\" WITH LOGIN PASSWORD :'secret';\n\\endif\n")
	} else {
		b.WriteString("\\if :role_privileged\n\\echo " + pgRolePrivilegedMarker + "\n\\elif :role_exists\nALTER ROLE :\"role\" WITH LOGIN PASSWORD :'secret';\n\\else\n\\echo " + pgRoleMissingMarker + "\n\\endif\n")
	}
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-")
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.CombinedOutput()
	text := strings.ReplaceAll(string(out), secret, "[secret]")
	switch {
	case err != nil && strings.Contains(text, "already exists"):
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: "a role named " + role + " already exists"}
	case err != nil:
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "set role password: " + pgErrorLines(text)}
	case strings.Contains(text, pgRolePrivilegedMarker):
		return nil, &agentwire.AgentError{Code: agentwire.CodePermissionDenied, Message: "role " + role + " has server-wide rights; its password is not changed here"}
	case strings.Contains(text, pgRoleExistsMarker):
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: "a role named " + role + " already exists"}
	case strings.Contains(text, pgRoleMissingMarker):
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "no role named " + role}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// pgErrorLines keeps psql's ERROR lines from its output and drops the rest
// (the statement it echoes under LINE carries the values).
func pgErrorLines(out string) string {
	var keep []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "ERROR:") {
			keep = append(keep, strings.TrimSpace(ln))
		}
	}
	if len(keep) == 0 {
		return "psql failed"
	}
	return strings.Join(keep, "; ")
}

// ---- db.postgres.drop_role ----

type dbPgDropRoleParams struct {
	Role string `json:"role"`
}

func dbPgDropRoleHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgDropRoleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.Role) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid role name"}
	}
	sql := fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, p.Role)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "drop role: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// ---- db.postgres.grant ----

type dbPgGrantParams struct {
	DBName string `json:"db_name"`
	Role   string `json:"role"`
}

func dbPgGrantHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgGrantParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.DBName) || !pgValidIdent(p.Role) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid name"}
	}
	// Database-level grant (CONNECT/CREATE/TEMP) — run against the maintenance DB.
	sql := fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s"`, p.DBName, p.Role)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "grant: " + err.Error()}
	}
	// GH #1406: DATABASE-level alone is NOT usable — a Jabali PG database is owned
	// by postgres, and since PG15 revoked the default PUBLIC CREATE on `public`,
	// a "Full Access" db-user still gets "permission denied for schema public".
	// Grant the role full schema-level access on the target DB too (schema grants
	// only affect the connected DB, so this runs with -d <db>): CREATE+USAGE on
	// public, ALL on existing tables/sequences (postgres-owned + others), and
	// default privileges so future objects stay accessible. Same as the pgadmin
	// shadow fix (db.postgres.shadowadmin.grant_schema).
	role := `"` + strings.ReplaceAll(p.Role, `"`, `""`) + `"`
	schemaSQL := strings.Join([]string{
		"GRANT ALL ON SCHEMA public TO " + role,
		"GRANT ALL ON ALL TABLES IN SCHEMA public TO " + role,
		"GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO " + role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO " + role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO " + role,
	}, "; ") + ";"
	if err := pgRunSQLOnDB(ctx, p.DBName, schemaSQL); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "grant schema: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// pgRunSQLOnDB runs SQL against a SPECIFIC database (schema/object grants only
// affect the connected DB). dbName is pgValidIdent-checked by the caller and
// passed via -d, never interpolated into SQL.
func pgRunSQLOnDB(ctx context.Context, dbName, sql string) error {
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-d", dbName, "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql -d %s: %w (%s)", dbName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- db.postgres.revoke ----

func dbPgRevokeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgGrantParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.DBName) || !pgValidIdent(p.Role) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid name"}
	}
	sql := fmt.Sprintf(`REVOKE ALL PRIVILEGES ON DATABASE "%s" FROM "%s"`, p.DBName, p.Role)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "revoke: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// ---- db.postgres.list_dbs ----

type dbPgListResponse struct {
	Databases []string `json:"databases"`
}

func dbPgListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-XAtq",
		"-c", `SELECT datname FROM pg_database WHERE datistemplate = false AND datname != 'postgres' ORDER BY 1`)
	out, err := cmd.Output()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "list dbs: " + err.Error()}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	dbs := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			dbs = append(dbs, l)
		}
	}
	return dbPgListResponse{Databases: dbs}, nil
}

// ---- db.postgres.dump ----
//
// Backup helper. Dumps a single database to a file path the agent can
// later tar into the user's account_full backup. Caller specifies the
// output path; agent ensures parent dir exists with mode 0700.

type dbPgDumpParams struct {
	DBName  string `json:"db_name"`
	OutPath string `json:"out_path"`
}

func dbPgDumpHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgDumpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.DBName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid db name"}
	}
	if !strings.HasPrefix(p.OutPath, "/var/lib/jabali") && !strings.HasPrefix(p.OutPath, "/run/jabali") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "out_path must be under /var/lib/jabali or /run/jabali"}
	}
	// JAB-244: same dump-slot + host-floor admission as db.backup.
	// pg_dump opens its own output handle (-f), so there is no mid-dump
	// write cadence here — pre-checks + shared slots only.
	release, ok := dbBackupSlots.TryAcquire("pg:" + p.DBName)
	if !ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "too many concurrent database backups — retry shortly"}
	}
	defer release()
	if err := hostreserve.CheckReserve(filepath.Dir(p.OutPath), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "backup staging is under the host disk reserve: " + err.Error()}
	}
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "pg_dump",
		"-Fc", "--no-owner", "--no-privileges",
		"-f", p.OutPath, p.DBName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("pg_dump: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return dbPgCreateResponse{OK: true}, nil
}

// ---- db.postgres.revoke_public_access ----
//
// Converts the databases created before db.postgres.create_db revoked
// PUBLIC's CONNECT and TEMPORARY (see pgRevokePublicSQL). The maintenance
// database postgres and the templates are left alone: clients such as the
// database console connect to postgres first.
//
// PUBLIC is revoked only after each database's panel-granted roles hold
// their own database grant. A Postgres restore used to leave the tenant role
// with no CONNECT of its own (see pgRestorePostPass), so on a database
// restored before that fix the role reached its database through PUBLIC
// alone; revoking PUBLIC first would lock the tenant out.

// pgPublicAccessWhere selects the databases PUBLIC can still connect to or
// create temporary tables in.
const pgPublicAccessWhere = `NOT datistemplate AND datname <> 'postgres' AND ` +
	`(has_database_privilege('public', oid, 'CONNECT') OR has_database_privilege('public', oid, 'TEMPORARY'))`

type dbPgRevokePublicParams struct {
	// Grants maps a database to the roles the panel granted on it. Required
	// (an empty map means no grants), so a caller that forgot it cannot
	// revoke PUBLIC without re-granting first.
	Grants *map[string][]string `json:"grants"`
}

type dbPgRevokePublicResponse struct {
	Regranted []string `json:"regranted"`
	Revoked   []string `json:"revoked"`
}

func dbPgRevokePublicHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgRevokePublicParams
	if err := json.Unmarshal(params, &p); err != nil || p.Grants == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "grants is required"}
	}
	resp := dbPgRevokePublicResponse{Regranted: []string{}, Revoked: []string{}}

	// (1) Every panel-granted role gets its own database grant, the same
	// GRANT db.postgres.grant issues. A database or role that no longer
	// exists is skipped.
	dbs := make([]string, 0, len(*p.Grants))
	for db := range *p.Grants {
		dbs = append(dbs, db)
	}
	sort.Strings(dbs)
	for _, db := range dbs {
		if !pgValidIdent(db) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid database name"}
		}
		for _, role := range (*p.Grants)[db] {
			if !pgValidIdent(role) {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid role name"}
			}
			sql := fmt.Sprintf(`DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_database WHERE datname = '%[1]s') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[2]s') THEN
    GRANT ALL PRIVILEGES ON DATABASE "%[1]s" TO "%[2]s";
  END IF;
END$$;`, db, role)
			if err := pgRunSQL(ctx, sql); err != nil {
				// Stop before any revoke: this role would lose its database.
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("grant %s on %s: %v", role, db, err)}
			}
			resp.Regranted = append(resp.Regranted, role+" on "+db)
		}
	}

	// (2) Revoke PUBLIC wherever it still has access.
	out, err := execCommandContext(ctx, "sudo", "-u", "postgres", "psql", "-XAtq", "-c",
		"SELECT datname FROM pg_database WHERE "+pgPublicAccessWhere+" ORDER BY 1").Output()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "list databases: " + err.Error()}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			resp.Revoked = append(resp.Revoked, name)
		}
	}
	if len(resp.Revoked) == 0 {
		return resp, nil
	}
	// format('%I') quotes each name server-side, so a database created
	// outside the panel with an unusual name is covered too.
	sql := `DO $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT datname FROM pg_database WHERE ` + pgPublicAccessWhere + ` LOOP
    EXECUTE format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', r.datname);
  END LOOP;
END$$;`
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "revoke public access: " + err.Error()}
	}
	return resp, nil
}

func init() {
	Default.Register("db.postgres.create_db", dbPgCreateHandler)
	Default.Register("db.postgres.drop_db", dbPgDropHandler)
	Default.Register("db.postgres.create_role", dbPgCreateRoleHandler)
	Default.Register("db.postgres.drop_role", dbPgDropRoleHandler)
	Default.Register("db.postgres.grant", dbPgGrantHandler)
	Default.Register("db.postgres.revoke", dbPgRevokeHandler)
	Default.Register("db.postgres.list_dbs", dbPgListHandler)
	Default.Register("db.postgres.dump", dbPgDumpHandler)
	Default.Register("db.postgres.revoke_public_access", dbPgRevokePublicHandler)
}
