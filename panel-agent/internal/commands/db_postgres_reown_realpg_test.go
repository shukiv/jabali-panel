package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The reown's SQL against a real PostgreSQL (GH #2004). It needs the server
// binaries, so it runs only when JABALI_TEST_PG_BIN names their directory
// (for example /usr/lib/postgresql/17/bin); CI skips it. It starts a
// throwaway cluster on a unix socket, so it needs no root and no port.

type realPG struct {
	bin, sock, port string
}

func startRealPG(t *testing.T) *realPG {
	t.Helper()
	bin := os.Getenv("JABALI_TEST_PG_BIN")
	if bin == "" {
		t.Skip("JABALI_TEST_PG_BIN not set")
	}
	data := t.TempDir()
	sock, err := os.MkdirTemp("", "pgs") // a socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sock) })
	pg := &realPG{bin: bin, sock: sock, port: "54329"}
	if out, err := exec.Command(filepath.Join(bin, "initdb"), "-U", "postgres", "--auth=trust", "-D", data).CombinedOutput(); err != nil {
		t.Fatalf("initdb: %v\n%s", err, out)
	}
	opts := "-k " + sock + " -p " + pg.port + " -c listen_addresses=''"
	if out, err := exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-o", opts, "-w", "-l", filepath.Join(data, "log"), "start").CombinedOutput(); err != nil {
		t.Fatalf("pg_ctl start: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-m", "immediate", "stop").Run() })

	// The agent runs `sudo -u postgres psql ...`; here that is psql on the
	// throwaway cluster's socket.
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "sudo" && len(args) >= 3 && args[0] == "-u" && args[1] == "postgres" && args[2] == "psql" {
			return exec.CommandContext(ctx, filepath.Join(bin, "psql"), append([]string{"-h", sock, "-p", pg.port, "-U", "postgres"}, args[3:]...)...)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return pg
}

// sql runs statements as postgres in db.
func (pg *realPG) sql(t *testing.T, db, sql string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(pg.bin, "psql"), "-h", pg.sock, "-p", pg.port, "-U", "postgres",
		"-XAtq", "-v", "ON_ERROR_STOP=1", "-d", db, "-f", "-")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql %s: %v\n%s\n--- sql:\n%s", db, err, out, sql)
	}
	return strings.TrimSpace(string(out))
}

// owners returns object → owner for the fixture's objects in db.
func (pg *realPG) owners(t *testing.T, db string) map[string]string {
	t.Helper()
	out := pg.sql(t, db, `
SELECT 'schema '||nspname, nspowner::regrole FROM pg_namespace WHERE nspname IN ('app', 'public')
UNION ALL SELECT 'rel '||c.oid::regclass, c.relowner::regrole FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname IN ('public', 'app') AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
UNION ALL SELECT 'routine '||p.oid::regprocedure, p.proowner::regrole FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
  WHERE n.nspname = 'public' AND p.proname IN ('f', 'g', 'pr', 'agg', 'cfun', 'rng', 'rng_multi')
UNION ALL SELECT 'type '||t.oid::regtype, t.typowner::regrole FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
  WHERE n.nspname = 'public' AND t.typname IN ('d', 'e', 'comp', 'rng', 'rng_multi', 'citext')
UNION ALL SELECT 'lo '||oid, lomowner::regrole FROM pg_largeobject_metadata
UNION ALL SELECT 'stat '||stxname, stxowner::regrole FROM pg_statistic_ext
UNION ALL SELECT 'coll '||collname, collowner::regrole FROM pg_collation WHERE collname = 'coll'
UNION ALL SELECT 'conv '||conname, conowner::regrole FROM pg_conversion WHERE conname = 'conv'
UNION ALL SELECT 'op '||oid::regoperator, oprowner::regrole FROM pg_operator WHERE oprname = '==='
UNION ALL SELECT 'tscfg '||cfgname, cfgowner::regrole FROM pg_ts_config WHERE cfgname = 'cfg'
UNION ALL SELECT 'tsdict '||dictname, dictowner::regrole FROM pg_ts_dict WHERE dictname = 'dict'
ORDER BY 1`)
	m := map[string]string{}
	for _, ln := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(ln, "|"); ok {
			m[k] = v
		}
	}
	return m
}

// pgReownFixture is what a pre-GH #1993 restore left: everything created by
// postgres, an extension's objects too.
const pgReownFixture = `
CREATE EXTENSION citext;
CREATE TABLE t (id serial PRIMARY KEY, v text);
CREATE TABLE ident (id int GENERATED ALWAYS AS IDENTITY, v text);
CREATE SEQUENCE standalone_seq;
CREATE VIEW v AS SELECT * FROM t;
CREATE MATERIALIZED VIEW mv AS SELECT * FROM t;
CREATE TABLE parted (id int) PARTITION BY RANGE (id);
CREATE TABLE parted_1 PARTITION OF parted FOR VALUES FROM (0) TO (10);
CREATE SCHEMA app;
CREATE TABLE app.x (id int);
CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1';
CREATE FUNCTION g() RETURNS int LANGUAGE plpgsql AS $$BEGIN RETURN 1; END$$;
CREATE PROCEDURE pr() LANGUAGE sql AS 'SELECT 1';
CREATE AGGREGATE agg(int) (SFUNC = int4pl, STYPE = int);
CREATE FUNCTION cfun(int, int) RETURNS int LANGUAGE internal STRICT AS 'int4pl';
CREATE DOMAIN d AS int CHECK (VALUE > 0);
CREATE TYPE e AS ENUM ('a', 'b');
CREATE TYPE comp AS (a int, b text);
CREATE TYPE rng AS RANGE (subtype = int8, multirange_type_name = rng_multi);
SELECT lo_create(4242);
CREATE STATISTICS st ON id, v FROM t;
CREATE COLLATION coll (locale = 'C');
CREATE CONVERSION conv FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8;
CREATE OPERATOR === (LEFTARG = int, RIGHTARG = int, FUNCTION = int4eq);
CREATE TEXT SEARCH DICTIONARY dict (TEMPLATE = simple);
CREATE TEXT SEARCH CONFIGURATION cfg (COPY = simple);
INSERT INTO t (v) VALUES ('a'), ('b');
`

func TestPgReown_RealPostgres(t *testing.T) {
	pg := startRealPG(t)
	pg.sql(t, "postgres", `
CREATE ROLE alice_app LOGIN;
CREATE DATABASE alice_shop OWNER postgres;
CREATE DATABASE bob_blog OWNER postgres;
CREATE DATABASE carol_db OWNER postgres;
CREATE ROLE carol_admin LOGIN CREATEROLE;`)
	pg.sql(t, "alice_shop", pgReownFixture)
	pg.sql(t, "bob_blog", "CREATE TABLE b (id serial PRIMARY KEY);")
	pg.sql(t, "carol_db", "CREATE TABLE c (id int);")
	publicBefore := pg.owners(t, "alice_shop")["schema public"]

	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app", "bob_blog": "", "carol_db": "carol_admin", "gone_db": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}

	got := pg.owners(t, "alice_shop")
	want := map[string]string{
		"schema app":                    "alice_app",
		"schema public":                 publicBefore,
		"rel t":                         "alice_app",
		"rel t_id_seq":                  "alice_app",
		"rel ident":                     "alice_app",
		"rel ident_id_seq":              "alice_app",
		"rel standalone_seq":            "alice_app",
		"rel v":                         "alice_app",
		"rel mv":                        "alice_app",
		"rel parted":                    "alice_app",
		"rel parted_1":                  "alice_app",
		"rel app.x":                     "alice_app",
		"routine f()":                   "alice_app",
		"routine g()":                   "alice_app",
		"routine pr()":                  "alice_app",
		"routine agg(integer)":          "alice_app",
		"routine cfun(integer,integer)": "postgres",
		// A range type's constructors go with it.
		"routine rng(bigint,bigint)":      "alice_app",
		"routine rng(bigint,bigint,text)": "alice_app",
		"routine rng_multi()":             "alice_app",
		"routine rng_multi(rng)":          "alice_app",
		"routine rng_multi(rng[])":        "alice_app",
		"type d":                          "alice_app",
		"type e":                          "alice_app",
		"type comp":                       "alice_app",
		"type rng":                        "alice_app",
		"type rng_multi":                  "alice_app",
		"type citext":                     "postgres",
		"lo 4242":                         "alice_app",
		"stat st":                         "alice_app",
		"coll coll":                       "alice_app",
		"conv conv":                       "alice_app",
		"op ===(integer,integer)":         "alice_app",
		"tscfg cfg":                       "alice_app",
		"tsdict dict":                     "alice_app",
	}
	if !reflect.DeepEqual(got, want) {
		for k := range want {
			if got[k] != want[k] {
				t.Errorf("%s owned by %q, want %q", k, got[k], want[k])
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected %s owned by %q", k, got[k])
			}
		}
	}
	// The extension's own routines stay with it.
	if n := pg.sql(t, "alice_shop", "SELECT count(*) FROM pg_proc p JOIN pg_depend d ON d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e' WHERE p.proowner <> 'postgres'::regrole"); n != "0" {
		t.Errorf("%s of citext's routines changed owner", n)
	}
	// The reporter's case: the database user can reset the sequence.
	pg.sql(t, "alice_shop", "SET ROLE alice_app; TRUNCATE t RESTART IDENTITY;")

	holder := pgHolderRole("bob_blog")
	if o := pg.sql(t, "bob_blog", "SELECT relowner::regrole FROM pg_class WHERE relname = 'b'"); o != holder {
		t.Errorf("bob_blog's table owned by %q, want its holder %q", o, holder)
	}
	if login := pg.sql(t, "postgres", "SELECT rolcanlogin FROM pg_roles WHERE rolname = '"+holder+"'"); login != "f" {
		t.Errorf("holder rolcanlogin = %q", login)
	}
	// A role with server-wide rights gets nothing.
	if o := pg.sql(t, "carol_db", "SELECT relowner::regrole FROM pg_class WHERE relname = 'c'"); o != "postgres" {
		t.Errorf("carol_db's table owned by %q", o)
	}

	if resp.Reowned["alice_shop"] == 0 || resp.Reowned["bob_blog"] != 1 || resp.Failed["carol_db"] == "" || len(resp.Failed) != 1 {
		t.Errorf("response = %+v", resp)
	}
	if !reflect.DeepEqual(resp.Left["alice_shop"], []string{"cfun(integer,integer) (language internal)"}) {
		t.Errorf("left = %+v", resp.Left)
	}

	// Run again: nothing left to move.
	again, err := callPgReown(t, map[string]string{"alice_shop": "alice_app", "bob_blog": ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Reowned) != 0 || len(again.Failed) != 0 {
		t.Errorf("second run = %+v", again)
	}
}
