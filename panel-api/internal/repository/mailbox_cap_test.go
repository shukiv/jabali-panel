package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A mailbox belongs to its owner through its domain, so the email-account cap
// counts the owner's mailboxes across all their domains, leaving out the
// panel's system relays. CreateWithinCap holds the owner's users row FOR
// UPDATE around the count and the insert, like the other owner caps.
const mailboxCapCount = "SELECT COUNT(*) FROM mailboxes m JOIN domains d ON d.id = m.domain_id WHERE d.user_id = ? AND m.system = 0"

func TestMailboxCreateWithinCap_UnderCapInserts(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewMailboxRepository(db)
	mb := &models.Mailbox{ID: "m1", DomainID: "d1", LocalPart: "smtp"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u1"))
	mock.ExpectQuery(regexp.QuoteMeta(mailboxCapCount)).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec("INSERT INTO `mailboxes`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateWithinCap(context.Background(), mb, "u1", 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxCreateWithinCap_AtCapRejectsNoInsert(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewMailboxRepository(db)
	mb := &models.Mailbox{ID: "m1", DomainID: "d1", LocalPart: "smtp"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u1"))
	mock.ExpectQuery(regexp.QuoteMeta(mailboxCapCount)).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectRollback()

	err := repo.CreateWithinCap(context.Background(), mb, "u1", 3)
	require.ErrorIs(t, err, ErrMailboxCapExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxCreateWithinCap_OwnerMissing(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	err := repo.CreateWithinCap(context.Background(), &models.Mailbox{ID: "m1"}, "u1", 3)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxCountByOwner(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewMailboxRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta(mailboxCapCount)).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))

	n, err := repo.CountByOwner(context.Background(), "u1")
	require.NoError(t, err)
	require.Equal(t, int64(5), n)
	require.NoError(t, mock.ExpectationsWereMet())
}
