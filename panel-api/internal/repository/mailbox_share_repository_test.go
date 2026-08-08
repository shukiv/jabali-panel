package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMockShareDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT VERSION\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return gdb, mock, raw
}

// JAB-107: ListByUserID scopes shares to the owner in SQL via a two-hop join
// (mailbox_shares -> mailboxes -> domains on user_id) instead of a global
// window filtered in memory.
func TestMailboxShare_ListByUserID_ScopesInSQL(t *testing.T) {
	db, mock, raw := newMockShareDB(t)
	defer raw.Close()
	repo := NewMailboxShareRepository(db)

	mock.ExpectQuery(`SELECT count\(\*\) FROM .mailbox_shares. JOIN mailboxes ON mailboxes\.id = mailbox_shares\.owner_mailbox_id JOIN domains ON domains\.id = mailboxes\.domain_id WHERE domains\.user_id = \?`).
		WithArgs("user-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(631))

	mock.ExpectQuery(`SELECT mailbox_shares\.\* FROM .mailbox_shares. JOIN mailboxes ON mailboxes\.id = mailbox_shares\.owner_mailbox_id JOIN domains ON domains\.id = mailboxes\.domain_id WHERE domains\.user_id = \? ORDER BY mailbox_shares\.created_at DESC LIMIT`).
		WithArgs("user-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner_mailbox_id", "shared_with_mailbox_id", "created_at"}).
			AddRow("share-1", "own-1", "with-1", time.Now()))

	rows, total, err := repo.ListByUserID(context.Background(), "user-1",
		ListOptions{Offset: 500, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, int64(631), total)
	require.Len(t, rows, 1)
	require.Equal(t, "share-1", rows[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// DeleteByOwner must carry the owner scope in the SQL itself, and report
// ErrNotFound when the row belongs to someone else.
//
// The route is DELETE /mailboxes/:mbid/shares/:shareId. The handler
// authenticates :mbid, but the delete used to run on the bare :shareId, so any
// authenticated tenant who learned another tenant's share-row id could delete
// it by passing their OWN mailbox as :mbid — and the reconciler would then
// strip that share's JMAP shareWith on Stalwart. Scoping in the query (rather
// than a read-then-check in the handler) makes the authorization
// un-forgettable and un-raceable.
func TestMailboxShare_DeleteByOwner_ScopesInSQL(t *testing.T) {
	db, mock, raw := newMockShareDB(t)
	defer raw.Close()
	repo := NewMailboxShareRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM .mailbox_shares. WHERE id = \? AND owner_mailbox_id = \?`).
		WithArgs("share-1", "own-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.DeleteByOwner(context.Background(), "share-1", "own-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxShare_DeleteByOwner_ForeignRowIsNotFound(t *testing.T) {
	db, mock, raw := newMockShareDB(t)
	defer raw.Close()
	repo := NewMailboxShareRepository(db)

	mock.ExpectBegin()
	// Another tenant's share: the owner-scoped predicate matches nothing.
	mock.ExpectExec(`DELETE FROM .mailbox_shares. WHERE id = \? AND owner_mailbox_id = \?`).
		WithArgs("victim-share", "attacker-mailbox").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := repo.DeleteByOwner(context.Background(), "victim-share", "attacker-mailbox")
	require.ErrorIs(t, err, ErrNotFound,
		"deleting a share owned by another mailbox must not succeed")
	require.NoError(t, mock.ExpectationsWereMet())
}
