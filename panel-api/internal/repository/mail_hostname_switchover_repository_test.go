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

// JAB-390: the switchover completes in ONE transaction — applied mail
// hostname, the mail certificate row, and the request marked done — and only
// while the request still asks for the name being applied. A request that
// changed underneath (or vanished) must roll the whole flip back, never apply
// a name nobody asked for.

var swNow = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func TestMailHostnameSwitchover_CompleteIsOneTransaction(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailHostnameSwitchoverRepository(gdb)
	applied := "mx.example.net"

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `server_settings` SET `mail_hostname`=\\?,`updated_at`=\\? WHERE id = \\?").
		WithArgs("mx.example.net", sqlmock.AnyArg(), 1).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `panel_certificate` SET .*`hostname`=\\?.* WHERE kind = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `mail_hostname_switchover` SET .*`status`=\\?.* WHERE id = \\? AND desired = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Complete(context.Background(), "mx.example.net", &applied, swNow, swNow.Add(90*24*time.Hour), swNow)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailHostnameSwitchover_CompleteRollsBackWhenRequestChanged(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailHostnameSwitchoverRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `server_settings`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `panel_certificate`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `mail_hostname_switchover`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.Complete(context.Background(), "mx.example.net", nil, swNow, swNow, swNow)
	require.True(t, errors.Is(err, repository.ErrSwitchoverChanged), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailHostnameSwitchover_CancelInFlightIsRefused(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailHostnameSwitchoverRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mail_hostname_switchover` SET .* WHERE id = \\? AND status IN \\(\\?,\\?\\)").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM `mail_hostname_switchover`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "desired", "status"}).AddRow(1, "mx.example.net", "issuing"))

	err := repo.Cancel(context.Background(), swNow)
	require.True(t, errors.Is(err, repository.ErrSwitchoverInFlight), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailHostnameSwitchover_RequestInFlightIsRefused(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailHostnameSwitchoverRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mail_hostname_switchover` SET .* WHERE id = \\? AND status <> \\?").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM `mail_hostname_switchover`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "desired", "status"}).AddRow(1, "old.example.net", "issuing"))

	err := repo.Request(context.Background(), "mx.example.net", "admin:u1", swNow)
	require.True(t, errors.Is(err, repository.ErrSwitchoverInFlight), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailHostnameSwitchover_ClaimOnlyTheRequestedName(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailHostnameSwitchoverRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mail_hostname_switchover` SET .* WHERE id = \\? AND desired = \\? AND \\(status = \\? OR \\(status = \\? AND \\(next_retry_at IS NULL OR next_retry_at <= \\?\\)\\) OR \\(status = \\? AND updated_at < \\?\\)\\)").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	claimed, err := repo.Claim(context.Background(), "mx.example.net", swNow.Add(-10*time.Minute), swNow)
	require.NoError(t, err)
	require.False(t, claimed, "a row that no longer asks for the name, or is not due, is not claimed")
	require.NoError(t, mock.ExpectationsWereMet())
}
