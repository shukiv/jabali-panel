package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// A restored database's holder role (GH #1993). An account restore loads a
// PostgreSQL dump before the panel recreates the account's database users,
// so at load time there may be no role of the account's to own what the dump
// creates. The objects go to the database's holder instead: a role that
// can't sign in and has no server-wide rights. The first database user then
// granted on the database takes them over (db.postgres.grant), and the holder
// is dropped. Restored objects are never left owned by postgres: a routine
// owned by a superuser runs with a superuser's rights.
//
// A holder is told from a tenant's role of the same name by its attributes: a
// role the panel creates for a database user can always sign in.

// Markers the holder scripts print.
const (
	pgHolderForeignMarker = "JABALI_HOLDER_FOREIGN"
	pgHolderAdoptedMarker = "JABALI_HOLDER_ADOPTED"
)

// pgHolderAttrs are a holder's role attributes.
const pgHolderAttrs = "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"

// pgHolderMatch is true of a pg_roles row that is a holder: one with none of
// the rights a holder never has.
const pgHolderMatch = "NOT (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)"

// pgHolderRole derives database db's holder name: jbro_ + 16 hex of
// sha256(db), bounded like pgShadowRole.
func pgHolderRole(db string) string {
	sum := sha256.Sum256([]byte(db))
	return "jbro_" + hex.EncodeToString(sum[:])[:16]
}

// pgPinSearchPath starts every superuser script: names resolve in
// pg_catalog only. Inside a tenant's database the tenant can create
// functions and operators (in public, or in a schema named after the session
// user, "postgres"), and one that matches a call more closely than
// pg_catalog's would run with the superuser's rights (CVE-2018-1058). GH #2004.
const pgPinSearchPath = "SET search_path = pg_catalog, pg_temp;\n"

// pgRunScript runs a psql script as the postgres superuser, connected to db
// (the maintenance database when db is empty), and returns its output. The
// script reaches psql on stdin and takes its values as psql variables, so no
// SQL is built from them; each value passed pgValidIdent or is a derived
// name, so \set takes it as it is. It runs with the search path pinned to
// pg_catalog (pgPinSearchPath).
func pgRunScript(ctx context.Context, db, script string) (string, error) {
	args := []string{"-u", "postgres", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1"}
	if db != "" {
		args = append(args, "-d", db)
	}
	cmd := execCommandContext(ctx, "sudo", append(args, "-f", "-")...)
	cmd.Stdin = strings.NewReader(pgPinSearchPath + script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("psql: %w (%s)", err, pgErrorLines(string(out)))
	}
	return string(out), nil
}

// pgEnsureHolder creates holder, or finds it already there as a holder (an
// earlier restore's, which still owns its objects until a user takes them).
// A role of that name with any right a holder never has isn't one: the load
// stops rather than hand it the database.
func pgEnsureHolder(ctx context.Context, holder string) *agentwire.AgentError {
	script := "\\set holder '" + holder + "'\n" +
		"SELECT (count(*) > 0)::int AS holder_exists," +
		" (count(*) FILTER (WHERE NOT (" + pgHolderMatch + ")) > 0)::int AS holder_foreign" +
		" FROM pg_roles WHERE rolname = :'holder' \\gset\n" +
		"\\if :holder_foreign\n\\echo " + pgHolderForeignMarker + "\n" +
		"\\elif :holder_exists\n" +
		"\\else\nCREATE ROLE :\"holder\" WITH " + pgHolderAttrs + ";\n\\endif\n"
	out, err := pgRunScript(ctx, "", script)
	switch {
	case err != nil:
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "create holder role: " + err.Error()}
	case strings.Contains(out, pgHolderForeignMarker):
		return &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "a role named " + holder + " exists on this server and isn't a restore holder"}
	}
	return nil
}

// pgAdoptRestoredObjects hands database db's restored objects from its
// holder to role and drops the holder. It does nothing when db has no holder
// or role has server-wide rights (never a tenant's). It reports whether role
// took the objects over.
func pgAdoptRestoredObjects(ctx context.Context, db, role string) (bool, error) {
	script := "\\set holder '" + pgHolderRole(db) + "'\n" +
		"\\set role '" + role + "'\n" +
		"SELECT (count(*) FILTER (WHERE rolname = :'holder' AND " + pgHolderMatch + ") > 0)::int AS holder_ours," +
		" (count(*) FILTER (WHERE rolname = :'role' AND NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls)) > 0)::int AS role_plain" +
		" FROM pg_roles \\gset\n" +
		"\\if :holder_ours\n\\if :role_plain\n" +
		"BEGIN;\nREASSIGN OWNED BY :\"holder\" TO :\"role\";\nDROP OWNED BY :\"holder\";\nCOMMIT;\n" +
		"\\echo " + pgHolderAdoptedMarker + "\n" +
		// A holder that still owns something in another database stays.
		"\\set ON_ERROR_STOP off\nDROP ROLE :\"holder\";\n" +
		"\\endif\n\\endif\n"
	out, err := pgRunScript(ctx, db, script)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, pgHolderAdoptedMarker), nil
}

// pgDropIdleHolder drops database db's holder when it owns nothing any more,
// for instance once the database it held is gone. Best effort: a holder that
// still owns something stays, and so does a role of that name that isn't a
// holder.
func pgDropIdleHolder(ctx context.Context, db string) {
	script := "\\set holder '" + pgHolderRole(db) + "'\n" +
		"SELECT (count(*) > 0)::int AS holder_ours FROM pg_roles WHERE rolname = :'holder' AND " + pgHolderMatch + " \\gset\n" +
		"\\if :holder_ours\n\\set ON_ERROR_STOP off\nDROP ROLE :\"holder\";\n\\endif\n"
	_, _ = pgRunScript(ctx, "", script)
}

// pgConnectGrantees lists the roles that can connect to database db: those
// with CONNECT on it that sign in and have no server-wide rights. An account
// restore gives them the same access to the database it loads in db's place,
// which is a new database. db goes in as the -d argument: no SQL is built
// from it.
func pgConnectGrantees(ctx context.Context, db string) ([]string, error) {
	out, err := execCommandContext(ctx, "sudo", "-u", "postgres", "psql", "-XAtq", "-d", db, "-c",
		"SELECT DISTINCT r.rolname FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a"+
			" JOIN pg_roles r ON r.oid = a.grantee"+
			" WHERE d.datname = current_database() AND a.privilege_type = 'CONNECT' AND r.rolcanlogin"+
			" AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)"+
			" ORDER BY 1").Output()
	if err != nil {
		return nil, err
	}
	var roles []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			roles = append(roles, ln)
		}
	}
	return roles, nil
}
