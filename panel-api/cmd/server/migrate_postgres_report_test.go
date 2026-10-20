package main

import (
	"slices"
	"testing"
)

// The parser's postgres_unsupported entries become report lines of the
// databases stage; its other entries do not.
func TestPostgresNotMigrated(t *testing.T) {
	got := postgresNotMigrated([]string{
		"postgres_unsupported:db/shop_pg/shop_pg.pgsql.sql.zst",
		"zstd-web:web/x/domain_data.tar.zst:exit 1",
		"postgres_unsupported:cpmove-cp/psql/cp_app.tar",
	})
	want := []string{
		"databases: db/shop_pg/shop_pg.pgsql.sql.zst: PostgreSQL database not migrated (PostgreSQL databases are not carried over)",
		"databases: cpmove-cp/psql/cp_app.tar: PostgreSQL database not migrated (PostgreSQL databases are not carried over)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("postgresNotMigrated = %q, want %q", got, want)
	}
}
