package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ---- db.postgres.reown_superuser_objects (GH #2004) ----
//
// Before GH #1993 an account restore loaded a PostgreSQL dump as the postgres
// superuser, so everything it created stayed owned by postgres. The
// database's user could read and write rows, but not alter or drop the
// tables, or reset a sequence (TRUNCATE ... RESTART IDENTITY: "must be owner
// of sequence"). And a SECURITY DEFINER routine owned by a superuser runs
// with a superuser's rights. Restores now load as a role with no server-wide
// rights, but the databases restored before keep what they have.
//
// This hands what a superuser owns in each of the panel's databases to the
// database's user, or, for a database with none, to its holder role (see
// db_postgres_holder.go), which the first user granted on it takes over.
//
// What moves: schemas other than public, tables, views, materialized views,
// standalone sequences (a column's sequence follows its table), foreign
// tables, routines in a trusted language, aggregates, domains, enums,
// composite and range types with a range's constructors, large objects,
// statistics objects, collations, conversions, operators and text search
// configurations and dictionaries.
// What stays: whatever an extension created (it belongs to the extension),
// the public schema, and routines in an untrusted language such as C, which
// only a superuser can create. Those are reported as left.
//
// REASSIGN OWNED can't do this: it refuses the bootstrap superuser outright.
// Each database is changed in one transaction, so a failure leaves it as it
// was.

// Markers the reown scripts print.
const (
	pgReownCountMarker   = "JABALI_SUPEROWNED"
	pgReownLeftBegin     = "JABALI_LEFT_BEGIN"
	pgReownLeftEnd       = "JABALI_LEFT_END"
	pgReownDoneMarker    = "JABALI_REOWNED"
	pgReownRefusedMarker = "JABALI_REOWN_REFUSED"
)

// pgUserSchema is true of a namespace n that isn't one of the system's.
// Schema names starting with pg_ are reserved for the system.
const pgUserSchema = "n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'"

// pgNotExtensionMember is true of an object (catalog, oid) no extension
// created.
func pgNotExtensionMember(catalog, oid string) string {
	return "NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = '" + catalog + "'::regclass AND e.objid = " + oid + " AND e.deptype = 'e')"
}

// pgSuperOwner joins the owner column col to a superuser role.
func pgSuperOwner(col string) string {
	return " JOIN pg_roles o ON o.oid = " + col + " AND o.rolsuper"
}

// pgRoutineMoves is true of a routine p in language l that the reown hands
// over: one in a trusted language, an aggregate, or one a type made for
// itself (a range type's constructors), which a non-superuser creating the
// type owns too.
const pgRoutineMoves = "(l.lanpltrusted OR p.prokind = 'a' OR EXISTS (SELECT 1 FROM pg_depend i WHERE i.classid = 'pg_proc'::regclass" +
	" AND i.objid = p.oid AND i.refclassid = 'pg_type'::regclass AND i.deptype = 'i'))"

// pgReownKind is one kind of object the reown moves: the rows of the
// superuser-owned objects of that kind, and the statement that hands one to
// :'role'.
type pgReownKind struct {
	from string
	stmt string
}

var pgReownKinds = []pgReownKind{
	{ // schemas
		from: "pg_namespace n" + pgSuperOwner("n.nspowner") +
			" WHERE n.nspname <> 'public' AND " + pgUserSchema + " AND " + pgNotExtensionMember("pg_namespace", "n.oid"),
		stmt: "format('ALTER SCHEMA %I OWNER TO %I', n.nspname, :'role')",
	},
	{ // relations; a sequence that belongs to a column moves with its table
		from: "pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace" + pgSuperOwner("c.relowner") +
			" WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f') AND " + pgUserSchema + " AND " + pgNotExtensionMember("pg_class", "c.oid") +
			" AND NOT (c.relkind = 'S' AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid" +
			" AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')))",
		stmt: "format('ALTER %s %s OWNER TO %I', CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW'" +
			" WHEN 'S' THEN 'SEQUENCE' WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END, c.oid::regclass, :'role')",
	},
	{ // routines (see pgRoutineMoves)
		from: "pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace JOIN pg_language l ON l.oid = p.prolang" + pgSuperOwner("p.proowner") +
			" WHERE " + pgRoutineMoves + " AND " + pgUserSchema + " AND " + pgNotExtensionMember("pg_proc", "p.oid"),
		stmt: "format('ALTER ROUTINE %s OWNER TO %I', p.oid::regprocedure, :'role')",
	},
	{ // domains, enums, ranges and standalone composite types
		from: "pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace" + pgSuperOwner("t.typowner") +
			" WHERE (t.typtype IN ('d', 'e', 'r') OR (t.typtype = 'c' AND EXISTS (SELECT 1 FROM pg_class r WHERE r.oid = t.typrelid AND r.relkind = 'c')))" +
			" AND " + pgUserSchema + " AND " + pgNotExtensionMember("pg_type", "t.oid"),
		stmt: "format('ALTER %s %s OWNER TO %I', CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, t.oid::regtype, :'role')",
	},
	{ // large objects
		from: "pg_largeobject_metadata m" + pgSuperOwner("m.lomowner"),
		stmt: "format('ALTER LARGE OBJECT %s OWNER TO %I', m.oid, :'role')",
	},
	{ // statistics objects
		from: "pg_statistic_ext s JOIN pg_namespace n ON n.oid = s.stxnamespace" + pgSuperOwner("s.stxowner") +
			" WHERE " + pgUserSchema + " AND " + pgNotExtensionMember("pg_statistic_ext", "s.oid"),
		stmt: "format('ALTER STATISTICS %I.%I OWNER TO %I', n.nspname, s.stxname, :'role')",
	},
	{ // collations
		from: "pg_collation k JOIN pg_namespace n ON n.oid = k.collnamespace" + pgSuperOwner("k.collowner") +
			" WHERE " + pgUserSchema + " AND " + pgNotExtensionMember("pg_collation", "k.oid"),
		stmt: "format('ALTER COLLATION %I.%I OWNER TO %I', n.nspname, k.collname, :'role')",
	},
	{ // conversions
		from: "pg_conversion v JOIN pg_namespace n ON n.oid = v.connamespace" + pgSuperOwner("v.conowner") +
			" WHERE " + pgUserSchema + " AND " + pgNotExtensionMember("pg_conversion", "v.oid"),
		stmt: "format('ALTER CONVERSION %I.%I OWNER TO %I', n.nspname, v.conname, :'role')",
	},
	{ // operators
		from: "pg_operator op JOIN pg_namespace n ON n.oid = op.oprnamespace" + pgSuperOwner("op.oprowner") +
			" WHERE " + pgUserSchema + " AND " + pgNotExtensionMember("pg_operator", "op.oid"),
		stmt: "format('ALTER OPERATOR %s OWNER TO %I', op.oid::regoperator, :'role')",
	},
	{ // text search configurations
		from: "pg_ts_config f JOIN pg_namespace n ON n.oid = f.cfgnamespace" + pgSuperOwner("f.cfgowner") +
			" WHERE " + pgUserSchema + " AND " + pgNotExtensionMember("pg_ts_config", "f.oid"),
		stmt: "format('ALTER TEXT SEARCH CONFIGURATION %s OWNER TO %I', f.oid::regconfig, :'role')",
	},
	{ // text search dictionaries
		from: "pg_ts_dict y JOIN pg_namespace n ON n.oid = y.dictnamespace" + pgSuperOwner("y.dictowner") +
			" WHERE " + pgUserSchema + " AND " + pgNotExtensionMember("pg_ts_dict", "y.oid"),
		stmt: "format('ALTER TEXT SEARCH DICTIONARY %s OWNER TO %I', y.oid::regdictionary, :'role')",
	},
}

// pgReownLeftQuery lists the superuser-owned routines the reown leaves:
// those in an untrusted language, which only a superuser can create.
var pgReownLeftQuery = "SELECT format('%s (language %s)', p.oid::regprocedure, l.lanname)" +
	" FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace JOIN pg_language l ON l.oid = p.prolang" + pgSuperOwner("p.proowner") +
	" WHERE NOT " + pgRoutineMoves + " AND " + pgUserSchema + " AND " + pgNotExtensionMember("pg_proc", "p.oid") +
	" ORDER BY 1;\n"

// pgReownCountScript counts what the reown would move in a database and
// lists what it would leave.
func pgReownCountScript() string {
	parts := make([]string, len(pgReownKinds))
	for i, k := range pgReownKinds {
		parts[i] = "(SELECT count(*) FROM " + k.from + ")"
	}
	return "SELECT " + strings.Join(parts, " + ") + " AS superowned \\gset\n" +
		"\\echo " + pgReownCountMarker + " :superowned\n" +
		"\\pset tuples_only on\n\\pset format unaligned\n" +
		"\\echo " + pgReownLeftBegin + "\n" + pgReownLeftQuery + "\\echo " + pgReownLeftEnd + "\n"
}

// pgReownScript hands what a superuser owns in the database to role, in one
// transaction. Nothing moves unless role exists without server-wide rights.
// role is a pgValidIdent name or a derived holder name, so \set takes it as
// it is.
func pgReownScript(role string) string {
	var b strings.Builder
	b.WriteString("\\set role '" + role + "'\n")
	b.WriteString("SELECT (count(*) > 0)::int AS role_plain FROM pg_roles WHERE rolname = :'role'" +
		" AND NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls) \\gset\n")
	b.WriteString("\\if :role_plain\nBEGIN;\n")
	for _, k := range pgReownKinds {
		b.WriteString("SELECT " + k.stmt + " FROM " + k.from + " \\gexec\n")
	}
	b.WriteString("COMMIT;\n\\echo " + pgReownDoneMarker + "\n")
	b.WriteString("\\else\n\\echo " + pgReownRefusedMarker + "\n\\endif\n")
	return b.String()
}

type dbPgReownParams struct {
	// Databases maps each of the panel's PostgreSQL databases to the role
	// its objects belong to: the database's user, or "" when it has none.
	Databases *map[string]string `json:"databases"`
}

type dbPgReownResponse struct {
	// Reowned is, per database, how many objects were handed over. Only
	// databases where something moved are listed.
	Reowned map[string]int `json:"reowned"`
	// Left is, per database, the superuser-owned routines in an untrusted
	// language, which stay as they are.
	Left map[string][]string `json:"left"`
	// Failed is, per database, why nothing moved there.
	Failed map[string]string `json:"failed"`
}

// pgSystemDatabase is true of the databases PostgreSQL itself keeps.
func pgSystemDatabase(db string) bool {
	return db == "postgres" || db == "template0" || db == "template1"
}

// pgExistingDatabases lists the databases on the server.
func pgExistingDatabases(ctx context.Context) (map[string]bool, error) {
	out, err := execCommandContext(ctx, "sudo", "-u", "postgres", "psql", "-XAtq", "-c",
		"SELECT datname FROM pg_database").Output()
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			have[ln] = true
		}
	}
	return have, nil
}

// pgReownCount runs the count script in db: how many objects would move,
// and the routines that would stay.
func pgReownCount(ctx context.Context, db string) (int, []string, error) {
	out, err := pgRunScript(ctx, db, pgReownCountScript())
	if err != nil {
		return 0, nil, err
	}
	n := -1
	var left []string
	inLeft := false
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, pgReownCountMarker+" "):
			if v, cErr := strconv.Atoi(strings.TrimPrefix(ln, pgReownCountMarker+" ")); cErr == nil {
				n = v
			}
		case ln == pgReownLeftBegin:
			inLeft = true
		case ln == pgReownLeftEnd:
			inLeft = false
		case inLeft && ln != "":
			left = append(left, ln)
		}
	}
	if n < 0 {
		return 0, nil, fmt.Errorf("no count in the output")
	}
	return n, left, nil
}

func dbPgReownHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgReownParams
	if err := json.Unmarshal(params, &p); err != nil || p.Databases == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "databases is required"}
	}
	dbs := make([]string, 0, len(*p.Databases))
	for db, role := range *p.Databases {
		if !pgValidIdent(db) || pgSystemDatabase(db) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid database name"}
		}
		if role != "" && !pgValidIdent(role) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid role name"}
		}
		dbs = append(dbs, db)
	}
	sort.Strings(dbs)

	resp := dbPgReownResponse{Reowned: map[string]int{}, Left: map[string][]string{}, Failed: map[string]string{}}
	if len(dbs) == 0 {
		return resp, nil
	}
	have, err := pgExistingDatabases(ctx)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "list databases: " + err.Error()}
	}
	for _, db := range dbs {
		if !have[db] {
			continue // gone from the server; nothing to hand over
		}
		n, left, cErr := pgReownCount(ctx, db)
		if cErr != nil {
			resp.Failed[db] = "count superuser-owned objects: " + cErr.Error()
			continue
		}
		if len(left) > 0 {
			resp.Left[db] = left
		}
		if n == 0 {
			continue
		}
		role := (*p.Databases)[db]
		if role == "" {
			role = pgHolderRole(db)
			if aerr := pgEnsureHolder(ctx, role); aerr != nil {
				resp.Failed[db] = aerr.Message
				continue
			}
		}
		out, rErr := pgRunScript(ctx, db, pgReownScript(role))
		switch {
		case rErr != nil:
			resp.Failed[db] = "hand objects over: " + rErr.Error()
		case strings.Contains(out, pgReownRefusedMarker):
			resp.Failed[db] = "role " + role + " isn't on this server, or has server-wide rights"
		case !strings.Contains(out, pgReownDoneMarker):
			resp.Failed[db] = "hand objects over: no confirmation in the output"
		default:
			resp.Reowned[db] = n
		}
	}
	return resp, nil
}

func init() {
	Default.Register("db.postgres.reown_superuser_objects", dbPgReownHandler)
}
