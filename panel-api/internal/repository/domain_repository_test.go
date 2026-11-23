package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Rename flips the domain row AND, in the same transaction, resyncs the
// denormalized email_cached on shared MAILBOX resources — which (unlike
// mailboxes / mail_groups) has no AFTER UPDATE trigger and is maintained in Go.
// GH #1579 mail-carrying rename.
func TestDomain_Rename_ResyncsSharedResourceEmail(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET").
		WillReturnResult(sqlmock.NewResult(0, 1)) // 1 row renamed
	mock.ExpectExec("UPDATE shared_resources SET email_cached = CONCAT").
		WillReturnResult(sqlmock.NewResult(0, 2)) // 2 shared mailboxes resynced
	mock.ExpectCommit()

	err := repo.Rename(context.Background(), "dom-1", "new.com", "/home/u1/public_html/new.com")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A rename of a missing row (0 rows affected) is ErrNotFound and rolls back —
// the shared-resource resync never runs.
func TestDomain_Rename_MissingRowIsNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET").
		WillReturnResult(sqlmock.NewResult(0, 0)) // no such row
	mock.ExpectRollback()

	err := repo.Rename(context.Background(), "gone", "new.com", "/home/u1/public_html/new.com")
	require.True(t, errors.Is(err, repository.ErrNotFound), "missing row → ErrNotFound, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Turning email on for a 'none'-provider domain makes Jabali its provider in
// the same UPDATE (no window where the row is email-on but provider 'none');
// a plain enable leaves the provider columns alone.
func TestDomain_UpdateEmailState_SwitchToJabali(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET `email_enabled`=\\?,`email_enabled_at`=\\?,`mail_provider`=\\?,`skip_auto_san`=\\?,`updated_at`=\\? WHERE id = \\?").
		WithArgs(true, sqlmock.AnyArg(), "jabali", false, sqlmock.AnyArg(), "dom-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.UpdateEmailState(context.Background(), "dom-1", repository.DomainEmailState{Enabled: true, SwitchToJabali: true}))

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET `email_enabled`=\\?,`email_enabled_at`=\\?,`updated_at`=\\? WHERE id = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.UpdateEmailState(context.Background(), "dom-1", repository.DomainEmailState{Enabled: true}))
	require.NoError(t, mock.ExpectationsWereMet())
}
