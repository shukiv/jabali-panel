//go:build integration

// Integration tests for the db package. Gated by the `integration` build
// tag so they're excluded from the default `go test ./...` run.
//
// Requires a reachable MariaDB + a DSN in JABALI_TEST_DATABASE_URL pointing
// at a disposable test database. The tests DROP + recreate all tables, so
// never point this at a production DB.
//
// Run locally via:
//   make test-integration

package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("JABALI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("JABALI_TEST_DATABASE_URL not set; skipping integration test")
	}
	return dsn
}

// dropAllTables drops every table in the test database, leaving it empty.
// The whole db integration suite (this file plus the dirty-recovery tests) now
// runs against ONE database in a single `go test` process — including in CI —
// so a test that does not start from a clean slate would inherit whatever a
// prior test left behind (a forced/dirty schema_migrations version, orphan
// rows). The old reset only dropped users/refresh_tokens/schema_migrations,
// which is a relic from when the schema was three tables; with ~140 tables it
// left most of them (and their data) standing, so migrations replayed onto a
// half-populated schema and failed with duplicate-column / FK errors. Drop
// EVERYTHING with FK checks off (dependency order is then irrelevant), pinned
// to a single connection so the session-scoped SET holds for every DROP.
func dropAllTables(t *testing.T, dsn string) {
	t.Helper()

	gdb, err := db.Open(db.Options{DSN: dsn, Silent: true})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1) // keep SET FOREIGN_KEY_CHECKS + the DROPs on one session

	var tables []string
	require.NoError(t, gdb.Raw(
		"SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()").
		Scan(&tables).Error)

	require.NoError(t, gdb.Exec("SET FOREIGN_KEY_CHECKS = 0").Error)
	for _, tbl := range tables {
		require.NoError(t, gdb.Exec("DROP TABLE IF EXISTS `"+tbl+"`").Error)
	}
	require.NoError(t, gdb.Exec("SET FOREIGN_KEY_CHECKS = 1").Error)
}

// resetSchema returns the test database to a freshly-migrated state, so a test
// is independent of prior runs' state.
func resetSchema(t *testing.T, dsn string) {
	t.Helper()
	dropAllTables(t, dsn)
	require.NoError(t, db.Migrate(dsn))
}

func TestIntegration_MigrateAndPing(t *testing.T) {
	dsn := testDSN(t)

	// Clean slate so this genuinely exercises a from-scratch migrate,
	// regardless of what an earlier test in the suite left behind.
	dropAllTables(t, dsn)

	require.NoError(t, db.Migrate(dsn))
	// Re-running Migrate is a no-op — prove it.
	require.NoError(t, db.Migrate(dsn))

	gdb, err := db.Open(db.Options{DSN: dsn, Silent: true})
	require.NoError(t, err)
	require.NoError(t, db.Ping(gdb))

	// Schema sanity: expected tables exist.
	var tables []string
	require.NoError(t, gdb.Raw(
		"SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name").
		Scan(&tables).Error)
	assert.Contains(t, tables, "users")
	assert.Contains(t, tables, "server_settings")
	assert.Contains(t, tables, "schema_migrations")

	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	_ = sqlDB.Close()
}

func TestIntegration_UserCRUD(t *testing.T) {
	dsn := testDSN(t)
	resetSchema(t, dsn)

	gdb, err := db.Open(db.Options{DSN: dsn, Silent: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := gdb.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	repo := repository.NewUserRepository(gdb)
	ctx := context.Background()

	// username is NOT NULL + uniquely indexed since migration 000164; give the
	// test user a unique one (a bare ULID fits varchar(32)).
	uname := ids.NewULID()
	u := &models.User{
		ID:           ids.NewULID(),
		Username:     &uname,
		Email:        "int-" + ids.NewULID() + "@example.com",
		PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx",
		NameFirst:    "Alice",
		NameLast:     "A.",
		IsAdmin:      false,
	}
	require.NoError(t, repo.Create(ctx, u))

	got, err := repo.FindByEmail(ctx, u.Email)
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	assert.Equal(t, "Alice", got.NameFirst)

	// Duplicate username → ErrConflict. Email is intentionally non-unique since
	// migration 000164 (login moved to username), so the uniqueness conflict is
	// now on the username; dup keeps u's username and takes a distinct email.
	dup := *u
	dup.ID = ids.NewULID()
	dup.Email = "int2-" + ids.NewULID() + "@example.com"
	err = repo.Create(ctx, &dup)
	require.Error(t, err)
	assert.ErrorIs(t, err, repository.ErrConflict)

	// Update — email change, is_admin intentionally ignored by this method.
	got.Email = "changed-" + ids.NewULID() + "@example.com"
	got.NameFirst = "Alicia"
	require.NoError(t, repo.Update(ctx, got))

	reloaded, err := repo.FindByID(ctx, got.ID)
	require.NoError(t, err)
	assert.Equal(t, "Alicia", reloaded.NameFirst)

	// List returns at least the one we created.
	list, total, err := repo.List(ctx, repository.ListOptions{Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, int64(1))
	assert.NotEmpty(t, list)

	// Delete — hard delete.
	require.NoError(t, repo.Delete(ctx, got.ID))
	_, err = repo.FindByID(ctx, got.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, repository.ErrNotFound)
}

// (Removed TestIntegration_RefreshTokenRotateSerialises: the RefreshToken model
// + repository no longer exist, so the test referenced deleted symbols and broke
// the `integration` build tag on main. Refresh-token rotation is gone from the
// codebase; nothing to cover here.)

func TestIntegration_DSN_InvalidFailsFast(t *testing.T) {
	_ = testDSN(t) // skip unless integration env configured
	_, err := db.Open(db.Options{DSN: "mysql://bogus:bogus@127.0.0.1:1/nope?parseTime=true"})
	// We don't assert the exact error text — drivers vary — just that it
	// surfaces a real error rather than returning a silently-broken handle.
	require.Error(t, err)
}
