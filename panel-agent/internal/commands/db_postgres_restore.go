package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// db.postgres.restore — load a tenant-uploaded .sql dump into a Postgres
// database (GH #1045 PostgreSQL parity). The MariaDB sibling is db.restore.
//
// SECURITY MODEL (mirrors loadMariaDBDumpScoped / JAB-239): the dump is
// attacker-controlled content. It is NEVER loaded as the postgres superuser —
// that would let `COPY ... FROM PROGRAM`, `CREATE FUNCTION ... LANGUAGE C`,
// cross-database reads, etc. run with cluster privileges. Instead the dump is
// streamed through a per-database, NON-superuser shadow role over the scram TCP
// loopback listener, with psql running as the unprivileged OS user `nobody`:
//
//   - shadow role jbsr_<hash>: LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
//     NOINHERIT, fresh single-use random password. As a non-superuser it can't
//     escalate; every statement in the dump is confined to this one database.
//   - psql runs as OS user `nobody` via SysProcAttr.Credential (NOT sudo —
//     env_reset would scrub PGPASSWORD, the same scar as MYSQL_PWD in JAB-239),
//     so psql client meta-commands (\!, \copy, \i) land on an account that can
//     touch nothing.
//   - the password rides in PGPASSWORD (off argv, single-use), auth is
//     scram-sha-256 on 127.0.0.1:5432 which install.sh already enables (default
//     Debian pg_hba) — no pg_hba change needed.
//
// The dump produced by db.postgres.backup is --no-owner --no-privileges, so it
// carries no ownership/GRANT statements a non-superuser couldn't run. Ownership
// is re-established afterwards by an agent-authored SUPERUSER post-pass, never
// from dump content.
//
// The restore is built in a THROWAWAY staging db and renamed onto the real name
// only after it fully succeeds — so a failed load (truncated upload, a -Fc
// custom-format dump psql can't parse, the call ctx expiring mid-load) leaves
// the tenant's real database untouched, never wiped-and-empty:
//
//   1. superuser: CREATE DATABASE <tmp> OWNER <shadow> (shadow owns public and
//      can create objects while loading).
//   2. load the dump as the shadow into <tmp>. On failure: drop <tmp> + shadow,
//      return — the real db was never touched.
//   3. superuser post-pass on <tmp>: REASSIGN OWNED BY <shadow> TO <owner_role>
//      (the DB's primary tenant role) so restored objects are tenant-owned;
//      re-apply DATABASE + table/sequence/default privileges to every granted
//      role (MariaDB's GRANT ON db.* covers all tables — Postgres needs this
//      explicitly); ALTER DATABASE OWNER TO postgres (jabali's create model);
//      drop the shadow. When there are no granted roles, ownership falls to
//      postgres.
//   4. swap: DROP the real db, RENAME <tmp> onto its name. A crash in the tiny
//      gap between these leaves the restored data in the jbrt_* db (manually
//      recoverable), never an empty database. CheckReserve guards the transient
//      2x-on-disk cost.

type dbPgRestoreParams struct {
	DBName string `json:"db_name"`
	Path   string `json:"path"`
	// OwnerRole is the DB's primary tenant role — restored objects are
	// reassigned to it. Empty → objects owned by postgres (no tenant role to
	// hand them to).
	OwnerRole string `json:"owner_role,omitempty"`
	// GrantRoles are all tenant roles that had a grant on the DB before the
	// reset; each is re-granted DATABASE + object privileges after the load.
	GrantRoles []string `json:"grant_roles,omitempty"`
}

type dbPgRestoreResponse struct {
	OK bool `json:"ok"`
}

// pgShadowRole derives a bounded, deterministic loader role name: jbsr_ + 16 hex
// of sha256(db) = 21 chars, always under Postgres's 63-char NAMEDATALEN and
// pgValidIdent-safe regardless of how long or odd the database name is.
func pgShadowRole(db string) string {
	sum := sha256.Sum256([]byte(db))
	return "jbsr_" + hex.EncodeToString(sum[:])[:16]
}

// pgRestoreTmpDB derives the staging database name the dump is loaded into
// before the atomic swap: jbrt_ + 16 hex of sha256(db) = 21 chars, same bounds
// as pgShadowRole. Loading into a throwaway db and renaming on success means a
// FAILED load (truncated upload, a -Fc custom-format dump psql can't read, the
// call ctx expiring mid-load) leaves the tenant's real database untouched —
// never the drop-then-load data loss.
func pgRestoreTmpDB(db string) string {
	sum := sha256.Sum256([]byte(db))
	return "jbrt_" + hex.EncodeToString(sum[:])[:16]
}

// pgSuperExecInDB runs one statement as the postgres superuser CONNECTED TO db
// (REASSIGN OWNED / GRANT ON ALL TABLES / DROP OWNED are database-local). db is
// a pgValidIdent-checked identifier passed as an exec arg (no shell).
func pgSuperExecInDB(ctx context.Context, db, sql string) error {
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-d", db, "-c", sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql -d %s: %w (%s)", db, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func dbPgRestoreHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgRestoreParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if !pgValidIdent(p.DBName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid database name"}
	}
	if p.OwnerRole != "" && !pgValidIdent(p.OwnerRole) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid owner_role"}
	}
	for _, r := range p.GrantRoles {
		if !pgValidIdent(r) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid grant role: " + r}
		}
	}

	// Refuse to start loading when the PG data filesystem is already under the
	// host reserve floor (mirrors db.restore's /var/lib/mysql check).
	if err := hostreserve.CheckReserve("/var/lib/postgresql", 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "database storage is under the host disk reserve: " + err.Error()}
	}

	// Open the dump escape-proof under an allowed root (same scope + roots as
	// db.restore — Gitea #501 symlink hardening).
	restoreScope, scErr := filesafe.NewScope("system", "system", []string{
		"/var/lib/jabali/restore", "/var/lib/jabali-migrations", "/var/lib/jabali/migrations",
	})
	if scErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("scope: %v", scErr)}
	}
	p.Path = canonicalizeStagingRootPrefix(p.Path)
	f, err := restoreScope.Open(p.Path, os.O_RDONLY, 0)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "restore path invalid, outside an allowed directory, or not readable"}
	}
	defer f.Close()

	shadow := pgShadowRole(p.DBName)
	tmpDB := pgRestoreTmpDB(p.DBName)
	pb := make([]byte, 18)
	if _, err := rand.Read(pb); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "mint scoped restore password"}
	}
	pwd := hex.EncodeToString(pb) // hex only — safe in a SQL string literal

	// (1) Provision the NON-superuser shadow with a fresh single-use password.
	// DO block because CREATE ROLE has no IF NOT EXISTS; ALTER re-randomizes on
	// every restore so a captured credential is single-use. (The literal
	// password can appear in the server log only if an operator turns on
	// log_statement=ddl — same shape as the MariaDB loader; the role is
	// single-use + NOSUPERUSER regardless.)
	provision := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
    CREATE ROLE "%s" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD '%s';
  ELSE
    ALTER ROLE "%s" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD '%s';
  END IF;
END $$;`, shadow, shadow, pwd, shadow, pwd)
	if err := pgRunSQL(ctx, provision); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "provision scoped restore role: " + err.Error()}
	}
	// Until the swap succeeds, tear down anything we created — the STAGING db
	// and the shadow — never the tenant's real database. Dropping tmpDB first
	// removes the shadow's owned objects so DROP ROLE then succeeds. On success
	// this is a no-op (tmpDB was renamed away, shadow already dropped).
	success := false
	defer func() {
		if !success {
			_ = pgRunSQL(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, tmpDB))
			_ = pgRunSQL(context.Background(), fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, shadow))
		}
	}()

	// (2) Build the restore in a throwaway db owned by the shadow (so it can
	// create the dump's objects). DROP IF EXISTS first cleans up a tmpDB
	// stranded by a crashed prior attempt.
	if err := pgRunSQL(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, tmpDB)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "clear staging database: " + err.Error()}
	}
	if err := pgRunSQL(ctx, fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s" TEMPLATE template0`, tmpDB, shadow)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "create staging database: " + err.Error()}
	}

	// (3) Load the dump into the STAGING db as the shadow, unprivileged OS user,
	// over scram TCP. A failed load (bad/truncated/-Fc dump, ctx timeout) leaves
	// the tenant's real db untouched — the defer just drops the staging db.
	// Residual: `\!` in a dump runs as `nobody`, which can't touch the FS but
	// isn't network-isolated — the same accepted residual as the MariaDB loader.
	nobody, err := user.Lookup("nobody")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "lookup nobody: " + err.Error()}
	}
	uid, uerr := strconv.ParseUint(nobody.Uid, 10, 32)
	gid, gerr := strconv.ParseUint(nobody.Gid, 10, 32)
	if uerr != nil || gerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "parse nobody uid/gid"}
	}
	load := execCommandContext(ctx, "psql",
		"-v", "ON_ERROR_STOP=1", "-X", "-q",
		"-h", "127.0.0.1", "-p", "5432",
		"-U", shadow, "-d", tmpDB)
	load.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	load.Env = append(os.Environ(), "PGPASSWORD="+pwd)

	// Stream the dump through the ownership/privilege sanitizer (GH #1044) into
	// psql's stdin via a pipe: a stock `pg_dump` carries `ALTER ... OWNER TO` /
	// GRANT / SET SESSION AUTHORIZATION that the unprivileged shadow role can't
	// replay ("must be able to SET ROLE ..."), which ON_ERROR_STOP turns into a
	// whole-restore abort. psql (as `nobody`) reads the inherited pipe fd, never
	// the dump path — the same escape-proof property as handing it the file fd,
	// and the raw file is still only ever opened by root. The post-pass below
	// re-establishes tenant ownership + panel-tracked grants, so nothing dropped
	// here is lost.
	pr, pw, perr := os.Pipe()
	if perr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "restore pipe: " + perr.Error()}
	}
	load.Stdin = pr
	var out bytes.Buffer
	load.Stdout = &out
	load.Stderr = &out
	if err := load.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "restore start: " + err.Error()}
	}
	_ = pr.Close() // child holds its own copy; parent closes so psql sees EOF
	sanErr := sanitizePgPlainDump(f, pw)
	_ = pw.Close() // signal EOF to psql whatever the sanitize outcome
	if waitErr := load.Wait(); waitErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "restore load failed (check the dump is a plain-SQL pg_dump): " + strings.TrimSpace(out.String())}
	}
	if sanErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "restore stream: " + sanErr.Error()}
	}

	// (4) Superuser post-pass on the STAGING db — ownership + grants, none of it
	// from dump content. Everything here targets tmpDB; DATABASE grants attach
	// to its OID and survive the rename below.
	ownerTarget := p.OwnerRole
	if ownerTarget == "" {
		ownerTarget = "postgres"
	}
	if err := pgSuperExecInDB(ctx, tmpDB, fmt.Sprintf(`REASSIGN OWNED BY "%s" TO "%s"`, shadow, ownerTarget)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "reassign ownership: " + err.Error()}
	}
	// Re-apply privileges to every role that had a grant before the restore.
	// MariaDB's GRANT ON db.* implicitly covers every current + future table;
	// Postgres needs table/sequence + DEFAULT PRIVILEGES stated explicitly.
	for _, role := range p.GrantRoles {
		if err := pgRunSQL(ctx, fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s"`, tmpDB, role)); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "regrant database: " + err.Error()}
		}
		for _, stmt := range []string{
			`GRANT ALL ON SCHEMA public TO "%s"`,
			`GRANT ALL ON ALL TABLES IN SCHEMA public TO "%s"`,
			`GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO "%s"`,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO "%s"`,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO "%s"`,
		} {
			if err := pgSuperExecInDB(ctx, tmpDB, fmt.Sprintf(stmt, role)); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "regrant objects: " + err.Error()}
			}
		}
	}
	// Match jabali's create-time model: the database itself is postgres-owned.
	if err := pgRunSQL(ctx, fmt.Sprintf(`ALTER DATABASE "%s" OWNER TO postgres`, tmpDB)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "set database owner: " + err.Error()}
	}
	// Drop the shadow — DROP OWNED first clears its default-privilege ACLs in the
	// staging db (REASSIGN OWNED moved the objects, not the default-priv entries).
	_ = pgSuperExecInDB(ctx, tmpDB, fmt.Sprintf(`DROP OWNED BY "%s"`, shadow))
	if err := pgRunSQL(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, shadow)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "drop scoped role: " + err.Error()}
	}

	// (5) Atomic-ish swap: only now do we touch the tenant's real db — drop it
	// and rename the fully-built staging db onto its name. A crash in the gap
	// between these two leaves the restored data in the jbrt_* db (manually
	// recoverable), never an empty database. Terminate real-db connections so
	// the DROP can't block.
	if err := pgRunSQL(ctx, fmt.Sprintf(
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, p.DBName)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "terminate db connections: " + err.Error()}
	}
	if err := pgRunSQL(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, p.DBName)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "drop target database: " + err.Error()}
	}
	if err := pgRunSQL(ctx, fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, tmpDB, p.DBName)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "swap restored database: " + err.Error()}
	}
	success = true

	// Delete the uploaded dump through the scope (never a raw os.Remove — see
	// db.restore for the Gitea #501 rationale).
	_ = restoreScope.RemoveInScope(p.Path, false)

	return dbPgRestoreResponse{OK: true}, nil
}

func init() {
	Default.Register("db.postgres.restore", dbPgRestoreHandler)
}
