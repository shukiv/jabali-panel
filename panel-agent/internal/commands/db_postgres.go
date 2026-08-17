package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
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
	return dbPgCreateResponse{OK: true}, nil
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
}

func dbPgCreateRoleHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgCreateRoleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !pgValidIdent(p.Role) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid role name"}
	}
	if p.Password == "" || strings.ContainsAny(p.Password, "'\\\n\r") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid password (empty or contains forbidden chars)"}
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
	sql := fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s"`, p.DBName, p.Role)
	if err := pgRunSQL(ctx, sql); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "grant: " + err.Error()}
	}
	return dbPgCreateResponse{OK: true}, nil
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

func init() {
	Default.Register("db.postgres.create_db", dbPgCreateHandler)
	Default.Register("db.postgres.drop_db", dbPgDropHandler)
	Default.Register("db.postgres.create_role", dbPgCreateRoleHandler)
	Default.Register("db.postgres.drop_role", dbPgDropRoleHandler)
	Default.Register("db.postgres.grant", dbPgGrantHandler)
	Default.Register("db.postgres.revoke", dbPgRevokeHandler)
	Default.Register("db.postgres.list_dbs", dbPgListHandler)
	Default.Register("db.postgres.dump", dbPgDumpHandler)
}
