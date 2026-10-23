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

func TestBackupJobRepository_ListFinishedBackupsForDestination(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	before := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT `id`,`user_id`,`destination_id`,`kind`,`status`,`snapshot_id`,`created_at`,`finished_at` FROM `backup_jobs` "+
			"WHERE destination_id = ? AND kind IN (?,?) AND status IN (?,?) AND COALESCE(finished_at, created_at) < ? ORDER BY created_at ASC")).
		WithArgs("01DEST", models.BackupJobKindAccountBackup, models.BackupJobKindSystemBackup,
			models.BackupJobStatusSucceeded, models.BackupJobStatusPartial, before).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow("01JOB", models.BackupJobStatusSucceeded))

	rows, err := repo.ListFinishedBackupsForDestination(context.Background(), "01DEST",
		[]string{models.BackupJobStatusSucceeded, models.BackupJobStatusPartial}, before)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "01JOB", rows[0].ID)

	// No statuses is no rows, not an `IN ()` syntax error.
	rows, err = repo.ListFinishedBackupsForDestination(context.Background(), "01DEST", nil, before)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #2089: the Admin Backups list names a run's account and destination on
// the collapsed row. ListRuns reports user_id only when every job of the run
// belongs to one account (else NULL), destination_id when every job shares
// one destination (NULL also when that one destination is the implicit local
// repo), and destinations, the number of distinct destinations with NULL
// counted as local, so the UI can tell "Local" from "several".
func TestBackupJob_ListRuns_UserAndDestination(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(DISTINCT run_id) FROM backup_jobs WHERE run_id IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery(`(?s)CASE WHEN COUNT\(DISTINCT user_id\) = 1 THEN MAX\(user_id\) END\s+AS user_id.*` +
		`CASE WHEN COUNT\(DISTINCT COALESCE\(destination_id, ''\)\) = 1 THEN MAX\(destination_id\) END\s+AS destination_id.*` +
		`COUNT\(DISTINCT COALESCE\(destination_id, ''\)\)\s+AS destinations.*GROUP BY run_id`).
		WithArgs(50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"run_id", "kind", "user_id", "destination_id", "destinations"}).
			AddRow("run-one-account", "account_backup", "u1", "dest1", 1).
			AddRow("run-full-server", "system_backup", nil, nil, 1).
			AddRow("run-mixed-dest", "account_backup", "u1", nil, 2))

	runs, total, err := repo.ListRuns(context.Background(), 0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, runs, 3)

	require.NotNil(t, runs[0].UserID)
	require.Equal(t, "u1", *runs[0].UserID)
	require.NotNil(t, runs[0].DestinationID)
	require.Equal(t, "dest1", *runs[0].DestinationID)
	require.Equal(t, 1, runs[0].Destinations)

	require.Nil(t, runs[1].UserID, "a run over several accounts names none")
	require.Nil(t, runs[1].DestinationID)
	require.Equal(t, 1, runs[1].Destinations, "one destination, the implicit local repo")

	require.Nil(t, runs[2].DestinationID)
	require.Equal(t, 2, runs[2].Destinations)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #2245: Cancel seals only a job that is still queued or running. A job
// that ended meanwhile keeps its real outcome.
func TestBackupJob_MarkCancelledIfActive_GuardsOnActiveStatus(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	update := regexp.QuoteMeta("UPDATE `backup_jobs` SET") + ".*" +
		regexp.QuoteMeta("WHERE id = ? AND status IN (?,?)")
	mock.ExpectBegin()
	mock.ExpectExec(update).
		WithArgs("cancelled by an administrator", sqlmock.AnyArg(), models.BackupJobStatusCancelled,
			"j1", models.BackupJobStatusQueued, models.BackupJobStatusRunning).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	ok, err := repo.MarkCancelledIfActive(context.Background(), "j1", "cancelled by an administrator")
	require.NoError(t, err)
	require.True(t, ok, "an active job is sealed")

	mock.ExpectBegin()
	mock.ExpectExec(update).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	ok, err = repo.MarkCancelledIfActive(context.Background(), "j2", "cancelled by an administrator")
	require.NoError(t, err)
	require.False(t, ok, "a job that already ended is not sealed")
	require.NoError(t, mock.ExpectationsWereMet())
}
