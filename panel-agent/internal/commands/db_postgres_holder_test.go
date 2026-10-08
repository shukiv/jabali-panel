package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: an account restore loads a PostgreSQL dump as a role with no
// server-wide rights, never as postgres, and what the dump creates is owned
// by the database's holder role until the first database user granted on it
// takes it over.

// pgWorld records every command the agent runs and answers each from reply:
// what it prints and whether it fails. Each command's stdin is kept.
type pgWorld struct {
	dir   string
	lines []string
}

func newPgWorld(t *testing.T, reply func(line string) (string, bool)) *pgWorld {
	t.Helper()
	w := &pgWorld{dir: t.TempDir()}
	prevExec, prevAttr := execCommandContext, pgLoaderProcAttr
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		stdin := filepath.Join(w.dir, fmt.Sprintf("stdin-%d", len(w.lines)))
		w.lines = append(w.lines, line)
		out, fail := reply(line)
		code := "0"
		if fail {
			code = "1"
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", `cat > "$1"; printf '%s' "$0"; exit $2`, out, stdin, code)
	}
	// The loader runs as nobody, which a test can't switch to.
	pgLoaderProcAttr = func() (*syscall.SysProcAttr, *agentwire.AgentError) { return &syscall.SysProcAttr{}, nil }
	t.Cleanup(func() { execCommandContext, pgLoaderProcAttr = prevExec, prevAttr })
	return w
}

// stdin is what command i read.
func (w *pgWorld) stdin(t *testing.T, i int) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.dir, fmt.Sprintf("stdin-%d", i)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ran returns the index of the first command containing every part, or -1.
func (w *pgWorld) ran(parts ...string) int {
	for i, l := range w.lines {
		all := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				all = false
				break
			}
		}
		if all {
			return i
		}
	}
	return -1
}

// script returns the index of the first psql script whose stdin holds every
// part, or -1.
func (w *pgWorld) script(t *testing.T, parts ...string) int {
	t.Helper()
	for i, l := range w.lines {
		if !strings.HasSuffix(l, "-f -") {
			continue
		}
		s, all := w.stdin(t, i), true
		for _, p := range parts {
			if !strings.Contains(s, p) {
				all = false
				break
			}
		}
		if all {
			return i
		}
	}
	return -1
}

// pgExistsReply answers the "is there a database of this name" probe: yes
// for the databases in existing. ok is false for any other command.
func pgExistsReply(line string, existing []string) (out string, ok bool) {
	if !strings.Contains(line, "pg_database WHERE datname") {
		return "", false
	}
	for _, e := range existing {
		if strings.Contains(line, "'"+e+"'") {
			return "1", true
		}
	}
	return "", true
}

// restorePgAccount restores the PostgreSQL dumps of dbs from an upload into
// account me, which owns allowed.
func restorePgAccount(t *testing.T, me string, dbs, allowed []string) (*restoreClaims, *restoreReport, []string) {
	t.Helper()
	root := t.TempDir()
	var stages []backup.ManifestStage
	var results []backupRestoreStage
	for _, db := range dbs {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{db}})
		results = append(results, backupRestoreStage{Name: backup.StageDB, Status: backup.StageStatusOK})
		mustWrite(t, filepath.Join(root, "db", db+".pgdump"), "PGDMP")
	}
	claims, report := &restoreClaims{}, &restoreReport{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: allowed,
		ForeignDBNames: []string{}, Claims: claims, Report: report}
	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)
	return claims, report, warnings
}

func TestAccountRestore_LoadsPostgresAsARoleWithNoServerRights(t *testing.T) {
	me := currentUsername(t)
	db := me + "_pgapp"
	w := newPgWorld(t, func(line string) (string, bool) {
		out, _ := pgExistsReply(line, nil)
		return out, false
	})

	claims, report, warnings := restorePgAccount(t, me, []string{db}, []string{})

	if i := w.ran("-u postgres pg_restore"); i >= 0 {
		t.Errorf("the dump ran as postgres: %s", w.lines[i])
	}
	shadow, tmp, holder := pgShadowRole(db), pgRestoreTmpDB(db), pgHolderRole(db)
	if w.ran("pg_restore", "-U "+shadow, "-d "+tmp, "--no-owner", "--no-privileges", "--exit-on-error") < 0 {
		t.Errorf("the dump wasn't loaded into %s as %s; ran %v", tmp, shadow, w.lines)
	}
	if w.script(t, `\set holder '`+holder+`'`, `CREATE ROLE :"holder" WITH NOLOGIN NOSUPERUSER`) < 0 {
		t.Errorf("no holder role %s was made; ran %v", holder, w.lines)
	}
	if w.ran(`REASSIGN OWNED BY "`+shadow+`" TO "`+holder+`"`, "-d "+tmp) < 0 {
		t.Errorf("the restored objects weren't handed to the holder; ran %v", w.lines)
	}
	for _, l := range w.lines {
		if strings.Contains(l, "REASSIGN") && strings.Contains(l, `TO "postgres"`) {
			t.Errorf("the restored objects went to postgres: %s", l)
		}
	}
	if w.ran(`ALTER DATABASE "`+tmp+`" RENAME TO "`+db+`"`) < 0 {
		t.Errorf("the loaded database wasn't put in %s's place; ran %v", db, w.lines)
	}
	if strings.Join(claims.Databases, ",") != db || strings.Join(claims.ArchivePostgresDBs, ",") != db {
		t.Errorf("claims %+v (warnings %v), want %s restored and all the archive's", claims, warnings, db)
	}
	if strings.Join(report.PostgresDatabases, ",") != db {
		t.Errorf("reported PostgreSQL databases %v, want [%s]", report.PostgresDatabases, db)
	}
}

// A load that fails leaves the database as it was, so the restore doesn't
// claim it: the panel adds no row and makes no grant for it.
func TestAccountRestore_AFailedPostgresLoadLeavesTheDatabaseUnclaimed(t *testing.T) {
	me := currentUsername(t)
	good, broken := me+"_pggood", me+"_pgbroken"
	w := newPgWorld(t, func(line string) (string, bool) {
		if out, ok := pgExistsReply(line, []string{broken}); ok {
			return out, false
		}
		if strings.Contains(line, "pg_restore") && (strings.Contains(line, "-d "+pgRestoreTmpDB(broken)) || strings.HasSuffix(line, "-d "+broken)) {
			return "pg_restore: error: could not execute query", true
		}
		if strings.Contains(line, "pg_proc") {
			return "0", false
		}
		return "", false
	})

	claims, report, warnings := restorePgAccount(t, me, []string{good, broken}, []string{broken})

	if strings.Join(claims.Databases, ",") != good || strings.Join(claims.ArchivePostgresDBs, ",") != good {
		t.Errorf("claims %+v, want only %s", claims, good)
	}
	if strings.Join(report.PostgresDatabases, ",") != good {
		t.Errorf("reported %v, want only %s", report.PostgresDatabases, good)
	}
	if !hasWarning(warnings, "db "+broken+" (postgres): not restored: ") {
		t.Errorf("warnings %v should say %s wasn't restored", warnings, broken)
	}
	if i := w.ran(`DROP DATABASE IF EXISTS "` + broken + `"`); i >= 0 {
		t.Errorf("a failed load dropped the database: %s", w.lines[i])
	}
}

// The restored database is a new one: each role that could connect to the
// database it replaces gets the same access to it, and a role whose name the
// panel never uses is reported instead.
func TestAccountRestore_KeepsTheAccessOfTheRolesThatCanConnect(t *testing.T) {
	me := currentUsername(t)
	db := me + "_pgkeep"
	w := newPgWorld(t, func(line string) (string, bool) {
		if out, ok := pgExistsReply(line, []string{db}); ok {
			return out, false
		}
		if strings.Contains(line, "aclexplode") && strings.Contains(line, "-d "+db) {
			return me + "_app\n" + me + "_ro\nodd role\n", false
		}
		return "", false
	})

	_, _, warnings := restorePgAccount(t, me, []string{db}, []string{db})

	if i := w.ran("aclexplode", "rolcanlogin", "NOT (r.rolsuper"); i < 0 {
		t.Fatalf("the roles that can connect weren't read; ran %v", w.lines)
	}
	tmp := pgRestoreTmpDB(db)
	for _, role := range []string{me + "_app", me + "_ro"} {
		if w.ran(`GRANT ALL PRIVILEGES ON DATABASE "`+tmp+`" TO "`+role+`"`) < 0 {
			t.Errorf("%s wasn't granted the restored database; ran %v", role, w.lines)
		}
		if w.ran(`GRANT ALL ON ALL TABLES IN SCHEMA public TO "`+role+`"`, "-d "+tmp) < 0 {
			t.Errorf("%s wasn't granted the restored tables", role)
		}
	}
	if !hasWarning(warnings, `role "odd role" can connect to it now and won't after the restore`) {
		t.Errorf("warnings %v should name the role it can't grant", warnings)
	}
}

// A role named like the database's holder that can sign in, or has any
// server-wide right, isn't a holder: the load stops and the database stays as
// it was.
func TestPgLoadScoped_RefusesAHolderNameARoleAlreadyHas(t *testing.T) {
	db := "alice_pgx"
	// The holder check is the only psql script this load runs.
	w := newPgWorld(t, func(line string) (string, bool) {
		if strings.HasSuffix(line, "-f -") {
			return pgHolderForeignMarker, false
		}
		return "", false
	})

	err := pgLoadScoped(context.Background(), db, dumpFile(t), "", nil)

	if err == nil || err.Code != agentwire.CodeFailedPrecondition || !strings.Contains(err.Message, "isn't a restore holder") {
		t.Fatalf("got %v, want failed_precondition naming the role", err)
	}
	if i := w.ran("REASSIGN OWNED"); i >= 0 {
		t.Errorf("the objects were handed over anyway: %s", w.lines[i])
	}
	if i := w.ran(`DROP DATABASE IF EXISTS "` + db + `"`); i >= 0 {
		t.Errorf("the database was replaced: %s", w.lines[i])
	}
}

// db.postgres.restore with an owner hands the objects to it, as before, and
// drops a holder an earlier restore left on the database it replaced.
func TestPgLoadScoped_WithAnOwnerHandsTheObjectsToIt(t *testing.T) {
	db := "alice_pgy"
	w := newPgWorld(t, func(string) (string, bool) { return "", false })

	if err := pgLoadScoped(context.Background(), db, dumpFile(t), "alice_app", []string{"alice_app"}); err != nil {
		t.Fatal(err)
	}

	if w.ran(`REASSIGN OWNED BY "`+pgShadowRole(db)+`" TO "alice_app"`) < 0 {
		t.Errorf("the objects didn't go to the owner; ran %v", w.lines)
	}
	rename := w.ran(`RENAME TO "` + db + `"`)
	drop := w.script(t, `\set holder '`+pgHolderRole(db)+`'`, `DROP ROLE :"holder"`)
	if rename < 0 || drop < rename {
		t.Errorf("the idle holder wasn't dropped after the swap (rename %d, drop %d)", rename, drop)
	}
}

// A restore on the Databases page of a database no role is granted on leaves
// the objects with its holder, never with postgres.
func TestDBPgRestore_WithNoOwnerTheHolderKeepsTheObjects(t *testing.T) {
	db := "alice_pgn"
	w := newPgWorld(t, func(string) (string, bool) { return "", false })

	if err := pgLoadScoped(context.Background(), db, dumpFile(t), "", nil); err != nil {
		t.Fatal(err)
	}

	if w.ran(`REASSIGN OWNED BY "`+pgShadowRole(db)+`" TO "`+pgHolderRole(db)+`"`) < 0 {
		t.Errorf("the objects didn't go to the holder; ran %v", w.lines)
	}
	for _, l := range w.lines {
		if strings.Contains(l, "REASSIGN") && strings.Contains(l, `TO "postgres"`) {
			t.Errorf("the objects went to postgres: %s", l)
		}
	}
}

// dumpFile is a custom-format dump on disk.
func dumpFile(t *testing.T) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.pgdump")
	mustWrite(t, p, "PGDMP")
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func pgGrant(t *testing.T, db, role string) error {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"db_name": db, "role": role})
	_, err := dbPgGrantHandler(context.Background(), raw)
	return err
}

// The first user granted on a restored database takes over what its holder
// owns, and only from a holder: a role that can sign in or has server-wide
// rights is never handed or taken anything.
func TestDBPgGrant_TheFirstUserTakesOverTheRestoredObjects(t *testing.T) {
	w := newPgWorld(t, func(string) (string, bool) { return "", false })

	if err := pgGrant(t, "alice_pgz", "alice_app"); err != nil {
		t.Fatal(err)
	}

	i := w.script(t, `\set holder '`+pgHolderRole("alice_pgz")+`'`, `\set role 'alice_app'`)
	if i < 0 || !strings.Contains(w.lines[i], "-d alice_pgz") {
		t.Fatalf("no take-over ran in alice_pgz; ran %v", w.lines)
	}
	s := w.stdin(t, i)
	for _, want := range []string{
		"rolname = :'holder' AND " + pgHolderMatch,
		"rolname = :'role' AND NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls)",
		"\\if :holder_ours\n\\if :role_plain\n",
		`REASSIGN OWNED BY :"holder" TO :"role";`,
		`DROP OWNED BY :"holder";`,
		`DROP ROLE :"holder";`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("take-over script lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, `REASSIGN OWNED`) > strings.Index(s, `\endif`) {
		t.Errorf("the hand-over isn't inside the holder check:\n%s", s)
	}
	for _, need := range []string{"rolcanlogin", "rolsuper", "rolcreatedb", "rolcreaterole", "rolreplication", "rolbypassrls"} {
		if !strings.Contains(pgHolderMatch, need) {
			t.Errorf("a role with %s would count as a holder", need)
		}
	}
}

func TestDBPgGrant_ReportsATakeOverThatFails(t *testing.T) {
	newPgWorld(t, func(line string) (string, bool) {
		return "ERROR:  boom", strings.HasSuffix(line, "-f -")
	})
	if err := pgGrant(t, "alice_pgz", "alice_app"); err == nil || !strings.Contains(err.Error(), "take over restored objects") {
		t.Fatalf("got %v, want the take-over's failure", err)
	}
}

// Dropping a database drops its holder too, when the holder owns nothing
// else and is one.
func TestDBPgDrop_DropsTheDatabasesIdleHolder(t *testing.T) {
	w := newPgWorld(t, func(string) (string, bool) { return "", false })
	raw, _ := json.Marshal(map[string]string{"db_name": "alice_pgd"})
	if _, err := dbPgDropHandler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	drop := w.ran(`DROP DATABASE IF EXISTS "alice_pgd"`)
	i := w.script(t, `\set holder '`+pgHolderRole("alice_pgd")+`'`, pgHolderMatch, `DROP ROLE :"holder"`)
	if drop < 0 || i < drop {
		t.Fatalf("the holder wasn't dropped after the database (drop %d, holder %d); ran %v", drop, i, w.lines)
	}
}

func TestPgHolderRole(t *testing.T) {
	a, b := pgHolderRole("alice_db"), pgHolderRole("alice_db2")
	if !strings.HasPrefix(a, "jbro_") || len(a) != 21 || a == b || !pgValidIdent(a) {
		t.Errorf("holder names %q %q", a, b)
	}
	if a == pgShadowRole("alice_db") {
		t.Error("the holder and the loader share a name")
	}
}

// Every restore reply names the PostgreSQL databases it loaded, as a list:
// the panel grants each database user it has on them again.
func TestRestoreReplies_NameTheLoadedPostgresDatabases(t *testing.T) {
	for name, v := range map[string]any{
		"restore":           backupRestoreResult{RestoredPostgresDBs: []string{}},
		"restore_from_tar":  backupRestoreFromTarResult{RestoredPostgresDBs: []string{}},
		"restore_selective": backupRestoreSelectiveResult{RestoredPostgresDBs: []string{}},
	} {
		raw, _ := json.Marshal(v)
		if !strings.Contains(string(raw), `"restored_postgres_databases":[]`) {
			t.Errorf("%s reply %s lacks restored_postgres_databases", name, raw)
		}
	}
}
