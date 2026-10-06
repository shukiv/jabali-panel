package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newMockCronDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))

	gdb, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      raw,
		SkipInitializeWithVersion: false,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	return gdb, mock, raw
}

func TestCronJobCreate_Success(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)
	now := time.Now()

	job := &models.CronJob{
		ID:        "cron_abc123",
		UserID:    "user1",
		Name:      "hourly sync",
		Command:   "wp cron event run --due-now --path=/home/user1/example.com/public_html",
		Schedule:  "0 * * * *",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO `cron_jobs` (`id`,`user_id`,`name`,`command`,`schedule`,`enabled`,`run_as_root`,`last_run_at`,`last_exit_code`,`last_error`,`created_at`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) RETURNING `created_at`,`updated_at`")).
		WithArgs(
			job.ID, job.UserID, job.Name, job.Command, job.Schedule, job.Enabled,
			job.RunAsRoot,
			nil, nil, nil,
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "updated_at"}).
			AddRow(job.CreatedAt, job.UpdatedAt))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), job)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Regression: Enabled=false MUST be written to the INSERT, not dropped as
// a Go zero-value (the model carries `default:1`, so a plain GORM Create
// would silently land enabled=1 — the cPanel cron importer's
// disabled-import safety net was defeated this way).
func TestCronJobCreate_DisabledPersists(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)
	now := time.Now()
	job := &models.CronJob{
		ID:        "cron_disabled1",
		UserID:    "user1",
		Name:      "imported-1",
		Command:   "/usr/bin/wget https://example.com/ping",
		Schedule:  "*/10 * * * *",
		Enabled:   false, // must reach the DB as 0, not the default:1
		CreatedAt: now,
		UpdatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO `cron_jobs` (`id`,`user_id`,`name`,`command`,`schedule`,`enabled`,`run_as_root`,`last_run_at`,`last_exit_code`,`last_error`,`created_at`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) RETURNING `created_at`,`updated_at`")).
		WithArgs(
			job.ID, job.UserID, job.Name, job.Command, job.Schedule, false, false,
			nil, nil, nil,
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "updated_at"}).
			AddRow(now, now))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), job))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobFindByID_Found(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `cron_jobs` WHERE id = \\?.*LIMIT").
		WithArgs("cron_abc123", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "name", "command", "schedule", "enabled",
			"last_run_at", "last_exit_code", "last_error", "created_at", "updated_at",
		}).AddRow(
			"cron_abc123", "user1", "hourly sync",
			"wp cron event run --due-now --path=/home/user1/example.com/public_html",
			"0 * * * *", true,
			nil, nil, nil, now, now,
		))

	job, err := repo.FindByID(context.Background(), "cron_abc123")
	require.NoError(t, err)
	require.Equal(t, "cron_abc123", job.ID)
	require.Equal(t, "user1", job.UserID)
	require.Equal(t, "hourly sync", job.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobFindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)

	mock.ExpectQuery("SELECT .* FROM `cron_jobs` WHERE id = \\?.*LIMIT").
		WithArgs("nonexistent", 1).
		WillReturnError(gorm.ErrRecordNotFound)

	job, err := repo.FindByID(context.Background(), "nonexistent")
	require.Equal(t, ErrNotFound, err)
	require.Nil(t, job)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobListByUserID_Empty(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)

	mock.ExpectQuery("SELECT .* FROM `cron_jobs` WHERE user_id = \\?.*ORDER BY").
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "name", "command", "schedule", "enabled",
			"last_run_at", "last_exit_code", "last_error", "created_at", "updated_at",
		}))

	jobs, err := repo.ListByUserID(context.Background(), "user1")
	require.NoError(t, err)
	require.Empty(t, jobs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobListByUserID_Populated(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `cron_jobs` WHERE user_id = \\?.*ORDER BY").
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "name", "command", "schedule", "enabled",
			"last_run_at", "last_exit_code", "last_error", "created_at", "updated_at",
		}).
			AddRow(
				"cron_2", "user1", "job 2", "wp cron event run --path=/home/user1/example.com/public_html",
				"0 * * * *", true, nil, nil, nil, now.Add(1*time.Minute), now.Add(1*time.Minute),
			).
			AddRow(
				"cron_1", "user1", "job 1", "php /home/user1/example.com/public_html/script.php",
				"0 3 * * *", true, nil, nil, nil, now, now,
			))

	jobs, err := repo.ListByUserID(context.Background(), "user1")
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	require.Equal(t, "cron_2", jobs[0].ID)
	require.Equal(t, "cron_1", jobs[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobListAll(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `cron_jobs`.*ORDER BY").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "name", "command", "schedule", "enabled",
			"last_run_at", "last_exit_code", "last_error", "created_at", "updated_at",
		}).
			AddRow(
				"cron_abc", "user1", "job", "wp cron event run --path=/home/user1/example.com/public_html",
				"0 * * * *", true, nil, nil, nil, now, now,
			))

	jobs, err := repo.ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "cron_abc", jobs[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobDelete(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `cron_jobs` WHERE id = \\?").
		WithArgs("cron_abc123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "cron_abc123")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCronJobCascadeDelete_OnUserDelete(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)

	mock.ExpectQuery("SELECT .* FROM `cron_jobs` WHERE id = \\?.*LIMIT").
		WithArgs("cron_abc123", 1).
		WillReturnError(gorm.ErrRecordNotFound)

	job, err := repo.FindByID(context.Background(), "cron_abc123")
	require.Equal(t, ErrNotFound, err)
	require.Nil(t, job)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCronJobCreate_DuplicateIsConflict: a duplicate id surfaces as
// ErrConflict, so a backup restore re-run reports the job as already there
// instead of "create: Error 1062" (GH #1993).
func TestCronJobCreate_DuplicateIsConflict(t *testing.T) {
	db, mock, raw := newMockCronDB(t)
	defer raw.Close()

	repo := NewCronJobRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO `cron_jobs`")).
		WillReturnError(&mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry 'cron_dup' for key 'PRIMARY'"})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), &models.CronJob{ID: "cron_dup", UserID: "user1", Name: "n", Command: "true", Schedule: "* * * * *"})
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
