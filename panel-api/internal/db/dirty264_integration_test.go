//go:build integration

// Integration test for the GH #1094 / #1103 broken-migration-264 recovery.
// Gated by the `integration` build tag; needs a DISPOSABLE MariaDB in
// JABALI_TEST_DATABASE_URL (it Drop()s and re-migrates the schema).
//
// Internal package (not db_test) so it can drive newMigrator to an exact
// version — reproducing the *genuine* broken state (263 applied, 264
// half-applied, nothing above), which is what makes the recovery's re-migrate
// meaningful rather than a replay of already-applied migrations.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run BrokenFtp264

package db

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func ft264DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("JABALI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("JABALI_TEST_DATABASE_URL not set; skipping integration test")
	}
	return dsn
}

// migrateToExact wipes the schema and migrates to exactly `version`, leaving it
// clean at that version with nothing above applied. Two golang-migrate quirks
// force the shape: (1) after Drop() the SAME migrator can't Migrate — it reads
// the now-gone schema_migrations before recreating it — so a fresh migrator is
// needed; (2) the advisory GET_LOCK is per-connection, so the first migrator
// must be CLOSED (releasing the lock) before the second opens, or the second
// dies on "can't acquire lock".
func migrateToExact(t *testing.T, dsn string, version uint) {
	t.Helper()
	m1, close1, err := newMigrator(dsn)
	require.NoError(t, err)
	dropErr := m1.Drop()
	close1() // release the lock + connection before the next migrator opens
	require.NoError(t, dropErr)

	m2, close2, err := newMigrator(dsn)
	require.NoError(t, err)
	defer close2()
	require.NoError(t, m2.Migrate(version))
}

func rawExec(t *testing.T, dsn, stmt string) {
	t.Helper()
	driverDSN, err := ToDriverDSN(dsn)
	require.NoError(t, err)
	sqlDB, err := openRawSQL(driverDSN)
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	_, err = sqlDB.Exec(stmt)
	require.NoError(t, err)
}

// TestBrokenFtp264_DetectAndRecover reproduces the genuine half-applied 264
// state and asserts the detector fires and the recovery lands the schema at head
// with the FTP objects the fixed migration creates.
func TestBrokenFtp264_DetectAndRecover(t *testing.T) {
	dsn := ft264DSN(t)

	// Genuine pre-264 state: 263 applied, clean, nothing above.
	migrateToExact(t, dsn, brokenFtp264PrevVersion)

	// Clean 263 must NOT look broken.
	broken, err := IsBrokenFtp264(dsn)
	require.NoError(t, err)
	require.False(t, broken, "clean schema at 263 must not match the broken-264 fingerprint")

	// Reproduce the failure: the CREATE TABLE committed, the server_settings
	// ALTER rolled back (row-size ceiling), migrate marked 264 dirty.
	rawExec(t, dsn, `CREATE TABLE ftp_accounts (
		id CHAR(26) NOT NULL PRIMARY KEY,
		user_id CHAR(26) NOT NULL,
		username VARCHAR(64) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	rawExec(t, dsn, `UPDATE schema_migrations SET version = 264, dirty = 1`)

	// Now it matches the fingerprint.
	broken, err = IsBrokenFtp264(dsn)
	require.NoError(t, err)
	require.True(t, broken, "dirty@264 + ftp_accounts + no server_settings.ftp_enabled must match")

	// Recover.
	recovered, err := RecoverBrokenFtp264(dsn)
	require.NoError(t, err)
	require.True(t, recovered)

	// Schema is clean, at (or past) 264, with both FTP artifacts present.
	st, err := State(dsn)
	require.NoError(t, err)
	require.False(t, st.Dirty, "schema must be clean after recovery")
	require.GreaterOrEqual(t, st.Version, uint(brokenFtp264Version))

	driverDSN, err := ToDriverDSN(dsn)
	require.NoError(t, err)
	sqlDB, err := openRawSQL(driverDSN)
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	hasFtp, err := tableExists(sqlDB, "ftp_accounts")
	require.NoError(t, err)
	require.True(t, hasFtp, "ftp_accounts must exist after the fixed 264 re-applies")
	hasCol, err := columnExists(sqlDB, "server_settings", "ftp_enabled")
	require.NoError(t, err)
	require.True(t, hasCol, "server_settings.ftp_enabled must exist after the fixed 264 re-applies")

	// Idempotent: a clean schema no longer matches, recovery is a no-op.
	broken, err = IsBrokenFtp264(dsn)
	require.NoError(t, err)
	require.False(t, broken)
	recovered, err = RecoverBrokenFtp264(dsn)
	require.NoError(t, err)
	require.False(t, recovered)
}

// TestBrokenFtp264_IgnoresOtherDirty: a dirty schema at a different version must
// not match — recovery stays operator-driven for anything but this exact bug.
func TestBrokenFtp264_IgnoresOtherDirty(t *testing.T) {
	dsn := ft264DSN(t)
	migrateToExact(t, dsn, brokenFtp264PrevVersion)
	rawExec(t, dsn, `UPDATE schema_migrations SET version = 200, dirty = 1`)
	broken, err := IsBrokenFtp264(dsn)
	require.NoError(t, err)
	require.False(t, broken, "dirty at a non-264 version must not match the fingerprint")
}
