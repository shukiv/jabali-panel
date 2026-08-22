package db

import (
	"database/sql"
	"fmt"
)

// The original migration 000264 (GH #1094, FTP/SFTP subaccounts) added
// `ftp_pasv_address VARCHAR(64)` to server_settings, which already sits at
// InnoDB's 65535-byte in-row ceiling — so the multi-column `ALTER TABLE
// server_settings` failed with ERROR 1118 ("Row size too large"). MySQL rolls
// the whole ALTER back, but the migration's EARLIER `CREATE TABLE ftp_accounts`
// statement had already committed. golang-migrate then marks the schema DIRTY at
// 264, and every later `migrate up` (i.e. every `jabali update` and every
// panel-api start) refuses to run — the panel is down and can't be updated.
//
// #1103 fixed the migration SQL (ftp_pasv_address TEXT, stored off-page), so
// FRESH installs apply 264 cleanly. But a host that already went dirty on the
// VARCHAR version is stuck: migrate won't run while dirty, and a blind re-run
// would hit "table ftp_accounts already exists". `jabali repair` deliberately
// won't force a dirty schema in general (forcing the wrong direction silently
// skips changes). This recovers the ONE precisely-identified broken state.

const brokenFtp264Version = 264
const brokenFtp264PrevVersion = 263

// IsBrokenFtp264 reports whether the schema is stuck in the exact half-applied
// state the original 000264 left behind: dirty at 264, `ftp_accounts` present
// (the CREATE TABLE that committed), but `server_settings.ftp_enabled` absent
// (proof the multi-column ALTER rolled back). Anything else — dirty at a
// different version, or 264-dirty without this precise fingerprint — returns
// false so the general operator-driven path still handles it.
func IsBrokenFtp264(dsn string) (bool, error) {
	st, err := State(dsn)
	if err != nil {
		return false, err
	}
	if !st.Dirty || st.Version != brokenFtp264Version {
		return false, nil
	}

	driverDSN, err := ToDriverDSN(dsn)
	if err != nil {
		return false, err
	}
	sqlDB, err := openRawSQL(driverDSN)
	if err != nil {
		return false, err
	}
	defer func() { _ = sqlDB.Close() }()

	hasFtpTable, err := tableExists(sqlDB, "ftp_accounts")
	if err != nil {
		return false, err
	}
	hasEnabledCol, err := columnExists(sqlDB, "server_settings", "ftp_enabled")
	if err != nil {
		return false, err
	}
	// The broken-VARCHAR fingerprint: the table committed, the server_settings
	// ALTER did not.
	return hasFtpTable && !hasEnabledCol, nil
}

// RecoverBrokenFtp264 clears the half-applied 000264 state and re-applies the
// corrected migration. It is gated on IsBrokenFtp264, so it is a no-op unless
// the precise fingerprint matches — never a blind force.
//
// Steps: DROP the orphan `ftp_accounts` (a brand-new, empty table created by the
// failed migration — no tenant data lives there), force the version back to 263
// (the last cleanly-applied migration), then `migrate up` so the fixed (TEXT)
// 000264 applies from scratch.
func RecoverBrokenFtp264(dsn string) (bool, error) {
	ok, err := IsBrokenFtp264(dsn)
	if err != nil || !ok {
		return false, err
	}

	driverDSN, err := ToDriverDSN(dsn)
	if err != nil {
		return false, err
	}
	sqlDB, err := openRawSQL(driverDSN)
	if err != nil {
		return false, err
	}
	// DROP the orphan table left by the committed CREATE TABLE. IF EXISTS keeps
	// this idempotent if a partial recovery already removed it.
	if _, err := sqlDB.Exec("DROP TABLE IF EXISTS ftp_accounts"); err != nil {
		_ = sqlDB.Close()
		return false, fmt.Errorf("drop orphan ftp_accounts: %w", err)
	}
	_ = sqlDB.Close()

	// Clear the dirty flag back to the last good version, then re-run migrations
	// so the corrected 264 (and anything after it) applies cleanly.
	if err := ForceVersion(dsn, brokenFtp264PrevVersion); err != nil {
		return false, fmt.Errorf("force version %d: %w", brokenFtp264PrevVersion, err)
	}
	if err := Migrate(dsn); err != nil {
		return false, fmt.Errorf("re-run migrations after recovery: %w", err)
	}
	return true, nil
}

// tableExists / columnExists probe information_schema in the connection's
// current database (DATABASE()).
func tableExists(db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = DATABASE() AND table_name = ?`, table,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("probe table %s: %w", table, err)
	}
	return n > 0, nil
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM information_schema.columns
		 WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("probe column %s.%s: %w", table, column, err)
	}
	return n > 0, nil
}
