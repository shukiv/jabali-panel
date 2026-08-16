package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newMockBackupDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return gdb, mock, raw
}

// Step 1 sanity-coverage. Step 6 orchestrator integration adds the
// state-transition tests once the wire shape is fixed by Step 2.

func TestBackupJob_Create_StampsDefaults(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `backup_jobs`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	job := &models.BackupJob{
		ID:          "01J5BACKUPJOB0000000000001",
		UserID:      "01J5USER0000000000000000001",
		Kind:        models.BackupJobKindAccountBackup,
		SystemdUnit: "jabali-backup-01J5BACKUPJOB0000000000001.service",
	}
	require.NoError(t, repo.Create(context.Background(), job))
	require.Equal(t, models.BackupJobStatusQueued, job.Status, "Create stamps default status")
	require.False(t, job.CreatedAt.IsZero(), "Create stamps CreatedAt")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackupJob_Get_NotFoundTranslated(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectQuery("SELECT .* FROM `backup_jobs` WHERE id = \\?").
		WithArgs("missing", 1).
		WillReturnError(gorm.ErrRecordNotFound)

	_, err := repo.Get(context.Background(), "missing")
	require.True(t, errors.Is(err, ErrNotFound), "ErrRecordNotFound translates to ErrNotFound")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackupJob_ListForUser_FiltersAndCounts(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `backup_jobs` WHERE user_id = \\?").
		WithArgs("01J5USER0000000000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	mock.ExpectQuery("SELECT \\* FROM `backup_jobs` WHERE user_id = \\? ORDER BY created_at DESC LIMIT \\?").
		WithArgs("01J5USER0000000000000000001", 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "kind", "status", "systemd_unit",
			"snapshot_id", "parent_snapshot", "bytes_added", "bytes_total",
			"manifest_json", "warnings_json", "error_text",
			"source_hostname", "source_panel_sha",
			"created_at", "started_at", "finished_at",
		}))

	rows, total, err := repo.ListForUser(context.Background(), "01J5USER0000000000000000001", 0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackupJobRepository_ListByStatusSince(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockBackupDB(t)
	defer raw.Close()

	repo := NewBackupJobRepository(gdb)
	since := time.Date(2026, 5, 13, 11, 30, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT \\* FROM `backup_jobs` WHERE status = \\? AND finished_at >= \\? ORDER BY finished_at ASC LIMIT \\?").
		WithArgs(models.BackupJobStatusFailed, since, 200).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "kind", "status"}).
			AddRow("j1", "u1", "account_backup", models.BackupJobStatusFailed))

	rows, err := repo.ListByStatusSince(context.Background(), models.BackupJobStatusFailed, since, 200)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "j1", rows[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackupJobRepository_ListByStatusSince_ZeroLimitDefaults(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockBackupDB(t)
	defer raw.Close()

	repo := NewBackupJobRepository(gdb)
	since := time.Date(2026, 5, 13, 11, 30, 0, 0, time.UTC)
	// limit=0 → impl defaults to 200; verify the LIMIT clause matches.
	mock.ExpectQuery("SELECT \\* FROM `backup_jobs` WHERE status = \\? AND finished_at >= \\? ORDER BY finished_at ASC LIMIT \\?").
		WithArgs(models.BackupJobStatusFailed, since, 200).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	rows, err := repo.ListByStatusSince(context.Background(), models.BackupJobStatusFailed, since, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

// silence unused-imports warnings on the time package if other tests
// later drop their reference.
var _ = errors.New

// GH #1045 (mrlp57): the retention-cap count must see only RETAINED account
// backups. The old CountForUser counted every job row ever — failed attempts,
// cancelled runs, restores — so early failures permanently consumed the cap
// and every scheduled backup silently rejected ("scheduled backups never
// fire, only manual"). Pin the WHERE shape.
func TestBackupJob_CountRetainedForUser_FiltersKindAndStatus(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectQuery(`SELECT count\(\*\) FROM .backup_jobs. WHERE user_id = \? AND kind = \? AND status IN \(\?,\?,\?,\?\)`).
		WithArgs("userA", models.BackupJobKindAccountBackup,
			models.BackupJobStatusQueued, models.BackupJobStatusRunning,
			models.BackupJobStatusSucceeded, models.BackupJobStatusPartial).
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))

	n, err := repo.CountRetainedForUser(context.Background(), "userA")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Prune must target the oldest RETAINED backup — forgetting a failed row
// frees no cap headroom and would loop the prune path.
func TestBackupJob_OldestAccountBackup_RetainedOnly(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectQuery(`SELECT \* FROM .backup_jobs. WHERE user_id = \? AND kind = \? AND status IN \(\?,\?\)`).
		WithArgs("userA", models.BackupJobKindAccountBackup,
			models.BackupJobStatusSucceeded, models.BackupJobStatusPartial, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow("j1", "userA"))

	j, err := repo.OldestAccountBackupForUser(context.Background(), "userA")
	require.NoError(t, err)
	require.Equal(t, "j1", j.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
