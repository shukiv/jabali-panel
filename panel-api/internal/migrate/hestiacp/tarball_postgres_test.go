package hestiacp

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A PostgreSQL database's dump is not a MariaDB dump: the parser leaves it out
// of MySQLDumps and records it as postgres_unsupported, on the zstd layout and
// on the plain one.
func TestParseHestiaTarball_PostgresDumpIsNotAMySQLDump(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not available")
	}
	stage := t.TempDir()
	files := map[string][]byte{"user.conf": []byte("USER='shop'\n")}
	for _, name := range []string{"shop_wp.mysql.sql", "shop_pg.pgsql.sql"} {
		dump := filepath.Join(stage, name)
		if err := os.WriteFile(dump, []byte("CREATE TABLE t(i INT);\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		zstdCompress(t, dump)
		db := strings.SplitN(name, ".", 2)[0]
		files["db/"+db+"/"+name+".zst"] = mustRead(t, dump+".zst")
	}
	// The plain layout of older Hestia versions.
	files["db/shop_old/shop_old.mysql.sql"] = []byte("CREATE TABLE t(i INT);\n")
	files["db/shop_oldpg/shop_oldpg.pgsql.sql"] = []byte("CREATE TABLE t(i INT);\n")
	outer := filepath.Join(stage, "shop.2026-10-20_10-00-00.tar")
	writeTar(t, outer, files)

	extractDir := filepath.Join(stage, "extract")
	got, err := ParseHestiaTarball(outer, extractDir)
	if err != nil {
		t.Fatalf("ParseHestiaTarball: %v", err)
	}
	var dumps []string
	for _, d := range got.MySQLDumps {
		dumps = append(dumps, filepath.Base(d))
	}
	slices.Sort(dumps)
	if want := []string{"shop_old.mysql.sql", "shop_wp.mysql.sql"}; !slices.Equal(dumps, want) {
		t.Errorf("MySQLDumps = %v, want %v", dumps, want)
	}
	for _, want := range []string{
		"postgres_unsupported:" + filepath.Join("db", "shop_pg", "shop_pg.pgsql.sql.zst"),
		"postgres_unsupported:" + filepath.Join("db", "shop_oldpg", "shop_oldpg.pgsql.sql"),
	} {
		if !slices.Contains(got.Skipped, want) {
			t.Errorf("Skipped = %v, want %q", got.Skipped, want)
		}
	}
	if _, err := os.Stat(filepath.Join(extractDir, "db", "shop_pg", "shop_pg.pgsql.sql")); err == nil {
		t.Error("the PostgreSQL dump was decompressed")
	}
}
