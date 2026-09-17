//go:build integration

// Integration test for the GH #1766 interrupted-migration-301 recovery.
// Gated by the `integration` build tag; needs a DISPOSABLE MariaDB in
// JABALI_TEST_DATABASE_URL (it Drop()s and re-migrates the schema).
//
// Internal package (not db_test) so it can drive newMigrator to an exact
// version — reproducing the genuine interrupted state (300 applied, 301
// half-run, nothing above) for each of the two interruption outcomes.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run BrokenMailHostname301
//
// migrateToExact / rawExec / ft264DSN are shared with dirty264_integration_test.go
// (same package + build tag).

package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBrokenMailHostname301_ColumnAbsent reproduces the interruption where the
// ADD COLUMN never applied: dirty at 301 with server_settings.mail_hostname
// absent. Recovery must force back to 300 and re-run 301 so the column lands.
func TestBrokenMailHostname301_ColumnAbsent(t *testing.T) {
	dsn := ft264DSN(t)

	// Genuine pre-301 state: 300 applied, clean, nothing above.
	migrateToExact(t, dsn, brokenMailHostname301PrevVersion)

	// Clean 300 must NOT look broken.
	broken, err := IsBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.False(t, broken, "clean schema at 300 must not match the dirty-301 fingerprint")

	// Reproduce the interruption BEFORE the ALTER committed: the column is
	// absent, migrate marked (301, dirty).
	rawExec(t, dsn, `UPDATE schema_migrations SET version = 301, dirty = 1`)

	broken, err = IsBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.True(t, broken, "dirty@301 must match")

	recovered, err := RecoverBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.True(t, recovered)

	// Clean, at (or past) 301, with the column now present (301 re-applied).
	assertRecovered301(t, dsn)

	// Idempotent: a clean schema no longer matches, recovery is a no-op.
	broken, err = IsBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.False(t, broken)
	recovered, err = RecoverBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.False(t, recovered)
}

// TestBrokenMailHostname301_ColumnPresent reproduces the interruption where the
// ADD COLUMN committed but the post-run dirty-flag clear was lost: dirty at 301
// with the column already present. Recovery must force forward to 301 (no SQL)
// — it must NOT drop the column or re-run the ALTER.
func TestBrokenMailHostname301_ColumnPresent(t *testing.T) {
	dsn := ft264DSN(t)

	// Fully apply the corrected 301 (captcha->TEXT + ADD mail_hostname) via the
	// real migration path, so the column is present and captcha is off-page —
	// the genuine committed state. A naked `ADD COLUMN mail_hostname` at v300
	// would itself hit ERROR 1118 (that is the #1766 bug), so this must go
	// through the migration, not a hand-rolled ALTER.
	migrateToExact(t, dsn, brokenMailHostname301Version)
	// The ALTER committed, but the process died before clearing the dirty flag.
	rawExec(t, dsn, `UPDATE schema_migrations SET version = 301, dirty = 1`)

	broken, err := IsBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.True(t, broken)

	recovered, err := RecoverBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.True(t, recovered)

	assertRecovered301(t, dsn)
}

// TestBrokenMailHostname301_IgnoresOtherDirty: a dirty schema at a different
// version must not match — recovery stays operator-driven for anything but this
// exact interrupted migration.
func TestBrokenMailHostname301_IgnoresOtherDirty(t *testing.T) {
	dsn := ft264DSN(t)
	migrateToExact(t, dsn, brokenMailHostname301PrevVersion)
	rawExec(t, dsn, `UPDATE schema_migrations SET version = 300, dirty = 1`)
	broken, err := IsBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.False(t, broken, "dirty at a non-301 version must not match the fingerprint")
	recovered, err := RecoverBrokenMailHostname301(dsn)
	require.NoError(t, err)
	require.False(t, recovered, "recovery must be a no-op for a non-301 dirty version")
}

// assertRecovered301 asserts the schema is clean, at or past 301, with
// server_settings.mail_hostname present.
func assertRecovered301(t *testing.T, dsn string) {
	t.Helper()
	st, err := State(dsn)
	require.NoError(t, err)
	require.False(t, st.Dirty, "schema must be clean after recovery")
	require.GreaterOrEqual(t, st.Version, uint(brokenMailHostname301Version))

	driverDSN, err := ToDriverDSN(dsn)
	require.NoError(t, err)
	sqlDB, err := openRawSQL(driverDSN)
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	hasCol, err := columnExists(sqlDB, "server_settings", "mail_hostname")
	require.NoError(t, err)
	require.True(t, hasCol, "server_settings.mail_hostname must exist after recovery")
}
