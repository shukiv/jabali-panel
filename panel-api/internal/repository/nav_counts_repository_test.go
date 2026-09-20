package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// mockNavCommonSections queues the four non-backup count sections compute()
// runs before backups (domains SUM, then databases / ftp / cron COUNTs), each
// returning zero so a test only has to assert the backups count.
func mockNavCommonSections(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT COALESCE\(SUM`).
		WillReturnRows(sqlmock.NewRows([]string{"web", "mail", "dns"}).AddRow(0, 0, 0))
	mock.ExpectQuery("FROM `databases`").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery("FROM `ftp_accounts`").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery("FROM `cron_jobs`").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
}

// GH #1784: the admin (global) backups badge must be denominated in backup
// RUNS + manual jobs — matching the Admin Backups page (GET /admin/backup-runs)
// — not the fleet-wide count of per-account_backup child jobs. A run fans out
// to one child per account, so counting child jobs multiplied the badge
// (7 runs x 2 accounts = 14 vs a 7-row page).
func TestNavCounts_Global_BackupsCountsRunsPlusManual(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewNavCountsRepository(db)

	mockNavCommonSections(mock)
	// Admin backups = runs (DISTINCT run_id) + manual (run_id IS NULL). The old
	// code issued a single account_backup COUNT here; asserting these two
	// queries in order fails against it.
	mock.ExpectQuery(`COUNT\(DISTINCT run_id\)`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(7))
	mock.ExpectQuery("run_id IS NULL").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(2))

	out, err := repo.Global(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 9, out.Backups, "admin badge = runs(7) + manual(2), not per-account job count")
	require.NoError(t, mock.ExpectationsWereMet())
}

// The tenant (/me) badge is unchanged: the caller's own retained account_backup
// jobs, which equals their flat /me/backups list.
func TestNavCounts_ForUser_BackupsCountsAccountJobs(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewNavCountsRepository(db)

	mockNavCommonSections(mock)
	mock.ExpectQuery("FROM `backup_jobs` WHERE user_id = .* AND kind = .* AND status IN").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(3))

	out, err := repo.ForUser(context.Background(), "01J5USER0000000000000000001")
	require.NoError(t, err)
	require.EqualValues(t, 3, out.Backups, "tenant badge = per-user account_backup jobs")
	require.NoError(t, mock.ExpectationsWereMet())
}
