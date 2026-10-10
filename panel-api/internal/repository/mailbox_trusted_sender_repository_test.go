package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2017: a mailbox's trusted senders.

func TestTrustedSenderCreate(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	row := &models.MailboxTrustedSender{ID: "01HZTRUST0000000000000001A", MailboxID: "01HZMBOX000000000000000001", Address: "bob@example.com"}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `mailbox_trusted_senders`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), row))
	require.False(t, row.CreatedAt.IsZero(), "Create must stamp CreatedAt")
	require.NoError(t, mock.ExpectationsWereMet())
}

// The same address twice on one mailbox is ErrConflict, so the API answers
// 409 and a restore counts it as already there.
func TestTrustedSenderCreate_DuplicateIsConflict(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `mailbox_trusted_senders`").
		WillReturnError(&mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_mailbox_trusted_sender'"})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), &models.MailboxTrustedSender{ID: "x", MailboxID: "m", Address: "bob@example.com"})
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrustedSenderListByMailbox(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	mock.ExpectQuery("SELECT \\* FROM `mailbox_trusted_senders` WHERE mailbox_id = \\? ORDER BY address ASC").
		WithArgs("m1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "mailbox_id", "address"}).
			AddRow("a", "m1", "alice@x.com").
			AddRow("b", "m1", "bob@y.com"))

	rows, err := repo.ListByMailbox(context.Background(), "m1")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "alice@x.com", rows[0].Address)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrustedSenderListByMailboxIDs(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	mock.ExpectQuery("SELECT \\* FROM `mailbox_trusted_senders` WHERE mailbox_id IN \\(\\?,\\?\\) ORDER BY mailbox_id ASC, address ASC").
		WithArgs("m1", "m2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "mailbox_id", "address"}).AddRow("a", "m2", "alice@x.com"))

	rows, err := repo.ListByMailboxIDs(context.Background(), []string{"m1", "m2"})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// No ids, no query.
	rows, err = repo.ListByMailboxIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrustedSenderListAll(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	mock.ExpectQuery("SELECT \\* FROM `mailbox_trusted_senders` ORDER BY mailbox_id ASC, address ASC").
		WillReturnRows(sqlmock.NewRows([]string{"id", "mailbox_id", "address"}).
			AddRow("a", "m1", "alice@x.com").
			AddRow("b", "m2", "bob@y.com"))

	rows, err := repo.ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrustedSenderCountByMailbox(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `mailbox_trusted_senders` WHERE mailbox_id = \\?").
		WithArgs("m1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))

	n, err := repo.CountByMailbox(context.Background(), "m1")
	require.NoError(t, err)
	require.EqualValues(t, 7, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Delete is scoped to the mailbox: an id of another mailbox's row deletes
// nothing and is ErrNotFound.
func TestTrustedSenderDelete(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxTrustedSenderRepository(db)
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `mailbox_trusted_senders` WHERE mailbox_id = \\? AND id = \\?").
		WithArgs("m1", "a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.Delete(context.Background(), "m1", "a"))

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `mailbox_trusted_senders` WHERE mailbox_id = \\? AND id = \\?").
		WithArgs("m1", "other").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	require.ErrorIs(t, repo.Delete(context.Background(), "m1", "other"), ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
