package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestBackupSchedule_Create_StampsTimestamps(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupScheduleRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `backup_schedules`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	uid := "01J5USER0000000000000000001"
	s := &models.BackupSchedule{
		ID:       "01J5SCHED0000000000000000A",
		Kind:     models.BackupScheduleKindAccount,
		UserID:   &uid,
		CronExpr: "0 3 * * *",
		Enabled:  true,
	}
	require.NoError(t, repo.Create(context.Background(), s))
	require.False(t, s.CreatedAt.IsZero())
	require.False(t, s.UpdatedAt.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackupSchedule_ListDue_FiltersOnEnabledAndNextRunAt(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupScheduleRepository(db)

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT \\* FROM `backup_schedules` WHERE enabled = \\? AND next_run_at IS NOT NULL AND next_run_at <= \\? ORDER BY next_run_at ASC LIMIT \\?").
		WithArgs(true, now, 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "kind", "user_id", "cron_expr", "enabled",
			"keep_daily", "keep_weekly", "keep_monthly",
			"last_run_at", "next_run_at", "created_at", "updated_at",
		}))

	rows, err := repo.ListDue(context.Background(), now, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackupSchedule_ReplaceDestinations_AtomicReplace(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupScheduleRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `backup_schedule_destinations` WHERE schedule_id = \\?").
		WithArgs("01J5SCHED0000000000000000A").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO `backup_schedule_destinations`").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	err := repo.ReplaceDestinations(context.Background(),
		"01J5SCHED0000000000000000A",
		[]string{"01J5DEST00000000000000000A", "01J5DEST00000000000000000B"})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #454: the Update column allowlist must include `content` — it was added to
// the model (#570) after this method was written, and an omitted column is a
// SILENT drop (the map-based Updates only writes listed columns). Regression
// guard: a tenant changing their scheduled-backup content must persist.
func TestBackupSchedule_Update_PersistsContent(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupScheduleRepository(db)

	mock.ExpectBegin()
	// Fails if `content` is missing from the SET clause (the bug). GORM orders
	// the map columns alphabetically, so `content` no longer sits right after
	// SET (`cadence` precedes it since GH #454 7B) — match it anywhere in SET.
	mock.ExpectExec("UPDATE .backup_schedules. SET .*.content.=").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	uid := "01J5USER0000000000000000001"
	s := &models.BackupSchedule{
		ID:       "01J5SCHED0000000000000000A",
		Kind:     models.BackupScheduleKindAccount,
		UserID:   &uid,
		CronExpr: "0 3 * * *",
		Content:  models.BackupContentFiles,
		Enabled:  true,
	}
	require.NoError(t, repo.Update(context.Background(), s))
	require.NoError(t, mock.ExpectationsWereMet())
}
