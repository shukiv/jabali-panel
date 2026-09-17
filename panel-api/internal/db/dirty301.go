package db

import "fmt"

// Migration 000301 (JAB-390) adds server_settings.mail_hostname. As first
// merged it was a single `ADD COLUMN mail_hostname TEXT NULL`, on the false
// assumption that an off-page TEXT column carries no row-size hazard.
//
// GH #1766: that assumption is wrong. InnoDB's 65535-byte row-size check counts
// the table's DEFINED in-row VARCHAR bytes even when the column being added is
// off-page TEXT, and server_settings already sits at that ceiling (~65 VARCHAR
// columns at v300). So on every innodb_strict_mode host the ALTER fails with
//
//	ERROR 1118 (42000): Row size too large. The maximum row size for the used
//	table type, not counting BLOBs, is 65535.
//
// MariaDB rolls the ALTER back, but golang-migrate's mysql driver has already
// written (301, dirty=true) — it sets the version dirty BEFORE running the SQL.
// The schema is left dirty at 301 with mail_hostname ABSENT, and every later
// `migrate up` (each panel-api start, each `jabali update`) refuses to run:
//
//	Error: migrate: migrate up: Dirty database version 301. Fix and force version.
//
// The panel is down and can't self-update — the same failure class as the
// original 000264 (#1094 -> #1103).
//
// The fixed 000301 relieves the ceiling first: it converts the two 512-byte
// crowdsec_captcha_* VARCHARs to off-page TEXT in the same ALTER, which frees
// enough in-row space for the ADD to succeed (verified against a real v300
// schema: 141 columns, strict mode). FRESH installs now apply 301 cleanly.
//
// A host already stuck on the broken (single-ADD) 301 is dirty at 301 with the
// column absent; migrate won't run while dirty. This recovers that ONE
// precisely-identified state by re-applying the corrected 301, mirroring
// RecoverBrokenFtp264. Only mail_hostname has ever occupied version 301 on a
// merged build (the file was renumbered 000300 -> 000301 in 614e1e680 and
// nothing else was 301), so "dirty at 301" is an unambiguous fingerprint.
//
// The mysql driver writes (301, dirty=true) before the ALTER and (301, dirty=
// false) after it succeeds, and the fixed ALTER is a single atomic DDL, so an
// interruption leaves EXACTLY one of two states — never a partial column:
//
//   - mail_hostname ABSENT: the ALTER did not apply (the #1766 stuck state, or
//     a fresh interruption before commit). Force the version back to 300 (the
//     last cleanly-applied migration) and re-run migrations so the corrected
//     301 applies from scratch.
//   - mail_hostname PRESENT: the ALTER committed and only the post-run flag
//     clear was lost. Force the version to 301 (bookkeeping only, no SQL) so
//     later migrations run.
//
// Both branches are safe because the column either fully exists or does not.

const brokenMailHostname301Version = 301
const brokenMailHostname301PrevVersion = 300

// IsBrokenMailHostname301 reports whether the schema is stuck dirty at exactly
// version 301 — the failed/interrupted mail_hostname migration (GH #1766). It
// does not probe the column here: dirty@301 is the stuck state regardless of
// which side of the ALTER the failure fell on, and the column probe (in
// RecoverBrokenMailHostname301) only selects the recovery branch. Any other
// version — dirty at a different migration, or clean — returns false so the
// general operator-driven dirty path (detectDirtyMigration) still handles it.
func IsBrokenMailHostname301(dsn string) (bool, error) {
	st, err := State(dsn)
	if err != nil {
		return false, err
	}
	return st.Dirty && st.Version == brokenMailHostname301Version, nil
}

// RecoverBrokenMailHostname301 clears the failed 000301 state and brings the
// schema to head. Gated on IsBrokenMailHostname301, so it is a no-op unless the
// schema is dirty at exactly 301 — never a blind force.
//
// It probes for server_settings.mail_hostname to tell the two states apart:
// absent => force back to 300 and let migrate re-apply the corrected 301
// (which relieves the row-size ceiling, so the re-run succeeds where the
// original single-ADD failed); present => force forward to 301 (bookkeeping
// only). Either way it finishes with a migrate up so anything above 301 lands.
func RecoverBrokenMailHostname301(dsn string) (bool, error) {
	ok, err := IsBrokenMailHostname301(dsn)
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
	hasCol, err := columnExists(sqlDB, "server_settings", "mail_hostname")
	_ = sqlDB.Close()
	if err != nil {
		return false, err
	}

	// Absent => the ALTER did not apply: drop back to the last good version so
	// migrate re-runs the corrected 301 from scratch. Present => the ADD
	// committed, only the flag clear was lost: assert 301, no SQL.
	target := brokenMailHostname301PrevVersion
	if hasCol {
		target = brokenMailHostname301Version
	}
	if err := ForceVersion(dsn, target); err != nil {
		return false, fmt.Errorf("force version %d: %w", target, err)
	}
	if err := Migrate(dsn); err != nil {
		return false, fmt.Errorf("re-run migrations after recovery: %w", err)
	}
	return true, nil
}
