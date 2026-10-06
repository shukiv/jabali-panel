package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a restore claims its uploaded backup, so a second restore or a
// delete is refused while it runs; a claim older than the restore deadline is
// stale and can be taken over.

var ubNow = time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)

func TestUploadedBackup_ClaimOnlyAnIdleRow(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewUploadedBackupRepository(gdb)
	stale := ubNow.Add(-65 * time.Minute)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `uploaded_backups` SET .*`restore_status`=\\?.* WHERE id = \\? AND \\(restore_status <> \\? OR restore_started_at IS NULL OR restore_started_at < \\?\\)").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "01K", "restoring", stale).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.ClaimRestore(context.Background(), "01K", "alice", ubNow, stale))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUploadedBackup_ClaimOfAHeldRowIsBusy(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewUploadedBackupRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `uploaded_backups`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM `uploaded_backups` WHERE id = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"id", "restore_status"}).AddRow("01K", "restoring"))

	err := repo.ClaimRestore(context.Background(), "01K", "alice", ubNow, ubNow.Add(-time.Hour))
	require.True(t, errors.Is(err, repository.ErrUploadedBackupBusy), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUploadedBackup_DeleteRefusesAHeldRow(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewUploadedBackupRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `uploaded_backups` WHERE id = \\? AND \\(restore_status <> \\?").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM `uploaded_backups` WHERE id = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"id", "restore_status"}).AddRow("01K", "restoring"))

	err := repo.DeleteIfIdle(context.Background(), "01K", ubNow.Add(-time.Hour))
	require.True(t, errors.Is(err, repository.ErrUploadedBackupBusy), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUploadedBackup_DeleteOfAMissingRowIsNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewUploadedBackupRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `uploaded_backups`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM `uploaded_backups`").WillReturnRows(sqlmock.NewRows([]string{"id"}))

	err := repo.DeleteIfIdle(context.Background(), "01K", ubNow)
	require.True(t, errors.Is(err, repository.ErrNotFound), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUploadedBackup_ListExpired(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewUploadedBackupRepository(gdb)

	mock.ExpectQuery("SELECT \\* FROM `uploaded_backups` WHERE expires_at IS NOT NULL AND expires_at < \\?").
		WithArgs(ubNow).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("01K"))

	rows, err := repo.ListExpired(context.Background(), ubNow)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}
