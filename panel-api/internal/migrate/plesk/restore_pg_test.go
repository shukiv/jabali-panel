package plesk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #429. A Plesk subscription backed by PostgreSQL failed the import with
//
//	pull-source: plesk db populate: stream db db_name: ssh stream
//	"MYSQL_PWD=\"$(cat /etc/psa/.psa.shadow)\" mysqldump -uadmin
//	--skip-lock-tables --single-transaction 'db_name'":
//	Process exited with status 2
//
// mysqldump was asked for a database that does not live in MySQL.
//
// describeDatabases already carried the right intent in its doc comment —
// "Postgres databases are recorded with a warning (v1 restore is MySQL-only,
// same as the other importers)" — but the code under it hardcoded
// Engine: "mysql" on every row and never read psa's data_bases.type. So a
// PostgreSQL database was relabelled MySQL and handed to mysqldump, and the
// operator got a bare exit-2 with nothing pointing at the cause.
//
// Until the destination can actually take a PostgreSQL dump (the agent has
// db.postgres.dump but no restore verb, and cpanel.ImportDatabases is
// MySQL-only), the honest behaviour is to detect the engine, leave those
// databases out of the mysqldump loop, and say so loudly enough that nobody
// tells a customer their database migrated.

func TestParseDatabasesManifest_CarriesEngine(t *testing.T) {
	t.Parallel()

	// New format is "<name>\t<engine>". Bare lines are pre-existing manifests
	// from a box that ran the old backup script — those predate any Postgres
	// awareness and were only ever produced for MySQL.
	got := parseDatabasesManifest("shop_db\tmysql\nblog_pg\tpostgresql\nlegacy_db\n\n")

	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(got), got)
	}
	for i, want := range []struct {
		name   string
		engine string
	}{
		{"shop_db", "mysql"},
		{"blog_pg", "postgresql"},
		{"legacy_db", "mysql"},
	} {
		if got[i].Name != want.name || got[i].Engine != want.engine {
			t.Errorf("entry %d = %+v, want {%s %s}", i, got[i], want.name, want.engine)
		}
	}
}

func TestPopulatePleskDBs_SkipsPostgresInsteadOfMysqldumpingIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "cpmove-acct")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := "shop_db\tmysql\nblog_pg\tpostgresql\n"
	if err := os.WriteFile(filepath.Join(base, "databases.txt"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	var cmds []string
	stream := func(cmd, dstPath string) error {
		cmds = append(cmds, cmd)
		return os.WriteFile(dstPath, []byte("-- dump\n"), 0o600)
	}

	n, skipped, err := PopulatePleskDBs(dir, "acct", stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("streamed %d database(s), want 1 — only the MySQL one", n)
	}
	if len(cmds) != 1 || !strings.Contains(cmds[0], "'shop_db'") {
		t.Fatalf("wrong command set: %v", cmds)
	}
	// The whole bug: mysqldump must never be pointed at the Postgres database.
	for _, c := range cmds {
		if strings.Contains(c, "blog_pg") {
			t.Errorf("PostgreSQL database was handed to a dump command:\n%s", c)
		}
	}
	if len(skipped) != 1 {
		t.Fatalf("want 1 skip record, got %v", skipped)
	}
	if !strings.Contains(skipped[0], "blog_pg") || !strings.Contains(skipped[0], "postgresql") {
		t.Errorf("skip record must name the database and its engine: %q", skipped[0])
	}
	if !strings.Contains(strings.ToLower(skipped[0]), "not migrated") {
		t.Errorf("skip record has to say the data did NOT come across — a "+
			"customer must never be told this database migrated: %q", skipped[0])
	}
}

func TestPopulatePleskDBs_AllMySQLReportsNoSkips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "cpmove-acct")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "databases.txt"), []byte("a_db\tmysql\nb_db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, skipped, err := PopulatePleskDBs(dir, "acct", func(cmd, dst string) error {
		return os.WriteFile(dst, []byte("x"), 0o600)
	})
	if err != nil || n != 2 || len(skipped) != 0 {
		t.Errorf("plain MySQL manifest regressed: n=%d skipped=%v err=%v", n, skipped, err)
	}
}

func TestDBDumpCommand_UnchangedForMySQL(t *testing.T) {
	t.Parallel()

	// The MySQL path is the one that works today; it must not drift.
	cmd := dbDumpCommand("shop_db")
	for _, want := range []string{"mysqldump", "-uadmin", "--single-transaction", "'shop_db'", "/etc/psa/.psa.shadow"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in:\n%s", want, cmd)
		}
	}
}
