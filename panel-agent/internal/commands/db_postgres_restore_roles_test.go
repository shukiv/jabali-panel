package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: a restore recreates a PostgreSQL database user's role, with the
// SCRAM-SHA-256 verifier its backup carried, and never takes over a role of
// that name that already exists. An upload restore names the PostgreSQL
// databases whose data, after it, is all the archive's, as it does for
// MariaDB, and the panel lets the archive grant access only to those.

const testSCRAM = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0$c3RvcmVka2V5c3RvcmVka2V5c3RvcmVka2V5:c2VydmVya2V5c2VydmVya2V5c2VydmVya2V5"

// stubPsqlScript runs every command as a shell that saves its stdin, prints
// out and exits with code. It returns where the stdin goes and the argvs.
func stubPsqlScript(t *testing.T, out string, code int) (string, *[][]string) {
	t.Helper()
	stdin := filepath.Join(t.TempDir(), "stdin")
	var calls [][]string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		script := `cat > "$1"; printf '%s' "$0"; exit ` + map[bool]string{true: "1", false: "0"}[code != 0]
		return exec.CommandContext(ctx, "/bin/sh", "-c", script, out, stdin)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return stdin, &calls
}

func readScript(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func pgCreateRole(t *testing.T, p map[string]any) error {
	t.Helper()
	raw, _ := json.Marshal(p)
	_, err := dbPgCreateRoleHandler(context.Background(), raw)
	return err
}

func pgAgentCode(err error) string {
	var ae *agentwire.AgentError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// create_only creates the role only where none of its name exists, and the
// script never alters one, with a verifier or with a password.
func TestDBPgCreateRole_CreateOnlyNeverChangesAnExistingRole(t *testing.T) {
	for name, p := range map[string]map[string]any{
		"verifier": {"role": "alice_u", "password_verifier": testSCRAM, "create_only": true},
		"password": {"role": "alice_u", "password": "Secret123x", "create_only": true},
	} {
		t.Run(name, func(t *testing.T) {
			stdin, calls := stubPsqlScript(t, "", 0)
			if err := pgCreateRole(t, p); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 1 || strings.Join((*calls)[0], " ") != "sudo -u postgres psql -X -q -v ON_ERROR_STOP=1 -f -" {
				t.Fatalf("ran %v", *calls)
			}
			s := readScript(t, stdin)
			if !strings.Contains(s, "\\if :role_exists\n\\echo JABALI_ROLE_EXISTS\n\\else\nCREATE ROLE :\"role\" WITH LOGIN PASSWORD :'secret';") ||
				strings.Contains(s, "ALTER ROLE") || strings.Contains(s, "DO $$") {
				t.Fatalf("create_only script:\n%s", s)
			}
		})
	}
}

func TestDBPgCreateRole_CreateOnlyReportsAnExistingRole(t *testing.T) {
	stubPsqlScript(t, "JABALI_ROLE_EXISTS\n", 0)
	if err := pgCreateRole(t, map[string]any{"role": "alice_u", "password_verifier": testSCRAM, "create_only": true}); pgAgentCode(err) != agentwire.CodeAlreadyExists {
		t.Fatalf("got %v, want already_exists", err)
	}
	// Created by someone else between the check and the CREATE.
	stubPsqlScript(t, `psql:<stdin>:5: ERROR:  role "alice_u" already exists`, 3)
	if err := pgCreateRole(t, map[string]any{"role": "alice_u", "password_verifier": testSCRAM, "create_only": true}); pgAgentCode(err) != agentwire.CodeAlreadyExists {
		t.Fatalf("got %v, want already_exists", err)
	}
}

// Without create_only, a verifier sets an existing role's password, never a
// role with server-wide rights, and reports a role that isn't there.
func TestDBPgCreateRole_VerifierSetsAnExistingRolesPassword(t *testing.T) {
	stdin, _ := stubPsqlScript(t, "", 0)
	if err := pgCreateRole(t, map[string]any{"role": "alice_u", "password_verifier": testSCRAM}); err != nil {
		t.Fatal(err)
	}
	s := readScript(t, stdin)
	if !strings.Contains(s, "\\if :role_privileged\n\\echo JABALI_ROLE_PRIVILEGED\n\\elif :role_exists\nALTER ROLE :\"role\" WITH LOGIN PASSWORD :'secret';") ||
		strings.Contains(s, "CREATE ROLE") || !strings.Contains(s, "rolsuper") {
		t.Fatalf("script:\n%s", s)
	}
	if !strings.Contains(s, "\\set secret '"+testSCRAM+"'\n") || !strings.Contains(s, "\\set role 'alice_u'\n") {
		t.Fatalf("the values must reach psql as variables:\n%s", s)
	}

	stubPsqlScript(t, "JABALI_ROLE_PRIVILEGED\n", 0)
	if err := pgCreateRole(t, map[string]any{"role": "postgres", "password_verifier": testSCRAM}); pgAgentCode(err) != agentwire.CodePermissionDenied {
		t.Fatalf("got %v, want permission_denied for a superuser", err)
	}
	stubPsqlScript(t, "JABALI_ROLE_MISSING\n", 0)
	if err := pgCreateRole(t, map[string]any{"role": "alice_u", "password_verifier": testSCRAM}); pgAgentCode(err) != agentwire.CodeNotFound {
		t.Fatalf("got %v, want not_found", err)
	}
}

// Only a SCRAM-SHA-256 verifier is taken, and nothing runs for another.
func TestDBPgCreateRole_RefusesAVerifierThatIsNotSCRAM(t *testing.T) {
	for _, v := range []string{
		"md5" + strings.Repeat("a", 32),
		testSCRAM + "'; ALTER ROLE postgres PASSWORD 'x",
		testSCRAM + "\n\\! id",
		"SCRAM-SHA-256$4096:salt",
	} {
		_, calls := stubPsqlScript(t, "", 0)
		err := pgCreateRole(t, map[string]any{"role": "alice_u", "password_verifier": v, "create_only": true})
		if pgAgentCode(err) != agentwire.CodeInvalidArgument || len(*calls) != 0 {
			t.Fatalf("verifier %q: got %v, ran %v; want invalid_argument and nothing run", v, err, *calls)
		}
	}
}

// psql echoes a failing statement; the error never carries the secret.
func TestDBPgCreateRole_ErrorNeverCarriesTheSecret(t *testing.T) {
	stubPsqlScript(t, "psql:<stdin>:5: ERROR:  syntax error near \""+testSCRAM+"\"\nLINE 1: ALTER ROLE x PASSWORD '"+testSCRAM+"'", 3)
	err := pgCreateRole(t, map[string]any{"role": "alice_u", "password_verifier": testSCRAM})
	if err == nil || strings.Contains(err.Error(), testSCRAM) || strings.Contains(err.Error(), "LINE 1") {
		t.Fatalf("error %v carries the secret", err)
	}
}

func TestAgentVersion_ReportsPGRoleCreateOnly(t *testing.T) {
	v, _ := agentVersionHandler(context.Background(), nil)
	for _, c := range v.(agentVersionResponse).Capabilities {
		if c == "pg_role_create_only" {
			return
		}
	}
	t.Fatal("agent.version must report pg_role_create_only")
}

// The bundle carries each PostgreSQL database user's SCRAM verifier, and
// nothing for an md5 one, another account's role, or a reserved name.
func TestEnrichPostgresRoleAuth_CarriesEachRolesSCRAMVerifier(t *testing.T) {
	calls := stubMySQL(t,
		"alice_pg\t"+testSCRAM+"\n"+
			"alice_md5\tmd5"+strings.Repeat("0", 32)+"\n"+
			"bob_pg\t"+testSCRAM+"\n"+
			"postgres\t"+testSCRAM+"\n")
	meta := &backup.AccountMetadata{DatabaseUsers: []backup.MetadataDatabaseUser{
		{Username: "alice_pg", Engine: "postgres"},
		{Username: "alice_md5", Engine: "postgres"},
		{Username: "alice_pg", Engine: "mariadb"},
		{Username: "postgres", Engine: "postgres"},
	}}
	if err := enrichPostgresRoleAuth(context.Background(), meta); err != nil {
		t.Fatal(err)
	}
	if got := meta.DatabaseUsers[0].PostgresPasswordVerifier; got != testSCRAM {
		t.Fatalf("alice_pg verifier %q", got)
	}
	for _, du := range meta.DatabaseUsers[1:] {
		if du.PostgresPasswordVerifier != "" {
			t.Fatalf("%s (%s) got verifier %q", du.Username, du.Engine, du.PostgresPasswordVerifier)
		}
	}
	if len(*calls) != 1 || strings.Contains(strings.Join((*calls)[0], " "), "alice") {
		t.Fatalf("want one query that names no role, got %v", *calls)
	}
}

// pgArgAfter is the argument after flag in args, or "".
func pgArgAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// pgLoads swaps the PostgreSQL dump load: it fails for the databases in
// fail.
func pgLoads(t *testing.T, fail ...string) {
	t.Helper()
	prev := loadRestoredPostgresDump
	loadRestoredPostgresDump = func(_ context.Context, db string, _ *os.File, _ []string) error {
		if containsString(fail, db) {
			return errors.New("restore load failed: pg_restore: error: could not execute query")
		}
		return nil
	}
	t.Cleanup(func() { loadRestoredPostgresDump = prev })
}

// pgRestoreWorld stubs PostgreSQL for a restore: the databases in existing
// exist, holds answers the "does it hold anything" check for a database (a
// shell script; "echo 0" when absent; the check fails on a database that
// doesn't exist), and the load fails for the databases in broken.
func pgRestoreWorld(t *testing.T, existing []string, holds map[string]func(query string) string, broken []string) {
	t.Helper()
	pgLoads(t, broken...)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		db := pgArgAfter(args, "-d")
		switch {
		case strings.Contains(line, "pg_database WHERE datname"):
			for _, e := range existing {
				if strings.Contains(line, "'"+e+"'") {
					return exec.CommandContext(ctx, "echo", "1")
				}
			}
			return exec.CommandContext(ctx, "true")
		case strings.Contains(line, "pg_proc"):
			if !containsString(existing, db) {
				return exec.CommandContext(ctx, "sh", "-c", "echo 'psql: error: database does not exist' >&2; exit 2")
			}
			if h, ok := holds[db]; ok {
				return exec.CommandContext(ctx, "sh", "-c", h(line))
			}
			return exec.CommandContext(ctx, "echo", "0")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
}

// restorePgUpload restores the PostgreSQL dumps of dbs from an upload; the
// account owns allowed.
func restorePgUpload(t *testing.T, me string, dbs, allowed []string, keep bool) *restoreClaims {
	t.Helper()
	root := t.TempDir()
	var stages []backup.ManifestStage
	var results []backupRestoreStage
	for _, db := range dbs {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{db}})
		results = append(results, backupRestoreStage{Name: backup.StageDB, Status: backup.StageStatusOK})
		mustWrite(t, filepath.Join(root, "db", db+".pgdump"), "PGDMP")
	}
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: allowed,
		ForeignDBNames: []string{}, Claims: claims, KeepExisting: keep}
	applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)
	return claims
}

func TestUploadRestore_NamesThePostgresDatabasesWhoseDataIsAllTheArchives(t *testing.T) {
	me := currentUsername(t)
	held, empty, fresh, broken, unchecked := me+"_pgheld", me+"_pgempty", me+"_pgfresh", me+"_pgbroken", me+"_pgunchecked"
	for _, keep := range []bool{true, false} {
		pgRestoreWorld(t, []string{held, empty, unchecked}, map[string]func(string) string{
			held:      func(string) string { return "echo 2" },
			unchecked: func(string) string { return "echo 'psql: error: connection failed' >&2; exit 2" },
		}, []string{broken})
		claims := restorePgUpload(t, me, []string{held, empty, fresh, broken, unchecked}, []string{held, empty, unchecked}, keep)
		if got := strings.Join(claims.ArchivePostgresDBs, ","); got != empty+","+fresh {
			t.Errorf("keep=%v: archive PostgreSQL databases %q, want only the empty and the new one (loaded %v)", keep, got, claims.Databases)
		}
		if len(claims.ArchiveMariaDBs) != 0 {
			t.Errorf("keep=%v: a PostgreSQL restore claimed MariaDB databases %v", keep, claims.ArchiveMariaDBs)
		}
	}
}

// A large object is data outside any table: a database with only one isn't
// empty.
func TestUploadRestore_APostgresDatabaseWithOnlyALargeObjectIsNotTheArchives(t *testing.T) {
	me := currentUsername(t)
	db := me + "_pglo"
	pgRestoreWorld(t, []string{db}, map[string]func(string) string{
		db: func(q string) string {
			if strings.Contains(q, "pg_largeobject_metadata") {
				return "echo 1"
			}
			return "echo 0"
		},
	}, nil)
	claims := restorePgUpload(t, me, []string{db}, []string{db}, false)
	if len(claims.Databases) != 1 || len(claims.ArchivePostgresDBs) != 0 {
		t.Fatalf("loaded %v, archive PostgreSQL databases %v; want it loaded and none", claims.Databases, claims.ArchivePostgresDBs)
	}
}

// The reply always names the archive's PostgreSQL databases in upload mode:
// the panel reads a missing list as an agent too old to say.
func TestRestoreFromTarResult_CarriesTheArchivePostgresDBs(t *testing.T) {
	for _, c := range []struct {
		claims *restoreClaims
		want   string
	}{
		{&restoreClaims{Databases: []string{"a_x"}, ArchivePostgresDBs: []string{"a_x"}}, `"archive_postgres_databases":["a_x"]`},
		{&restoreClaims{Databases: []string{"a_x"}}, `"archive_postgres_databases":[]`},
	} {
		var out backupRestoreFromTarResult
		out.setClaims(c.claims)
		raw, err := json.Marshal(out)
		if err != nil || !strings.Contains(string(raw), c.want) {
			t.Errorf("reply %s (%v), want %s", raw, err, c.want)
		}
	}
}
