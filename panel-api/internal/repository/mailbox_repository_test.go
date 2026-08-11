package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestMailboxRepository_FindByID_Found mirrors the database_user
// repo test. sqlmock-backed so we exercise the SQL shape without a
// real MariaDB dependency.
func TestMailboxRepository_FindByID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	cols := []string{
		"id", "domain_id", "local_part", "email_cached", "password_hash",
		"quota_bytes", "is_disabled", "last_usage_bytes", "last_usage_at",
		"created_at", "updated_at",
	}
	now := time.Now()
	mock.ExpectQuery("SELECT .* FROM `mailboxes` WHERE id = \\?.*LIMIT").
		WithArgs("mb_abc", 1).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(
			"mb_abc", "dom1", "alice", "alice@example.com", "$2b$12$hash",
			uint64(1<<30), false, uint64(0), nil, now, now,
		))

	mb, err := repo.FindByID(context.Background(), "mb_abc")
	require.NoError(t, err)
	require.NotNil(t, mb)
	require.Equal(t, "alice@example.com", mb.EmailCached)
	require.Equal(t, uint64(1<<30), mb.QuotaBytes)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_FindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	cols := []string{"id", "domain_id", "local_part", "email_cached", "password_hash",
		"quota_bytes", "is_disabled", "last_usage_bytes", "last_usage_at", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT .* FROM `mailboxes` WHERE id = \\?.*LIMIT").
		WithArgs("mb_missing", 1).
		WillReturnRows(sqlmock.NewRows(cols))

	mb, err := repo.FindByID(context.Background(), "mb_missing")
	require.Error(t, err)
	require.Nil(t, mb)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_Create(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	// We deliberately don't pin every arg — sqlmock would force us to
	// spell out the full INSERT column order, and the whole point of
	// leaving email_cached to the BEFORE INSERT trigger is that it
	// doesn't matter what we send here. Match the statement + commit.
	mock.ExpectExec("INSERT INTO `mailboxes`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	now := time.Now().UTC()
	err := repo.Create(context.Background(), &models.Mailbox{
		ID:           "mb_new",
		DomainID:     "dom1",
		LocalPart:    "bob",
		PasswordHash: "$2b$12$hash",
		QuotaBytes:   1 << 30,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_UpdatePasswordHash(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mailboxes` SET .* WHERE id = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdatePasswordHash(context.Background(), "mb_new", "$2b$12$newhash")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_UpdateQuota(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mailboxes` SET .* WHERE id = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdateQuota(context.Background(), "mb_new", 2<<30)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #371: SetSendOnly flips the send_only column so Stalwart's
// queryRecipient (which filters `send_only = 0`) stops delivering to it.
func TestMailboxRepository_SetSendOnly(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mailboxes` SET .*`send_only`.* WHERE id = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.SetSendOnly(context.Background(), "mb_new", true)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_Delete(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `mailboxes` WHERE id = \\?").
		WithArgs("mb_new").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Delete(context.Background(), "mb_new"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_ListByDomainID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	cols := []string{"id", "domain_id", "local_part", "email_cached", "password_hash",
		"quota_bytes", "is_disabled", "last_usage_bytes", "last_usage_at", "created_at", "updated_at"}
	now := time.Now()

	mock.ExpectQuery("SELECT count.* FROM `mailboxes`.*WHERE domain_id = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))

	mock.ExpectQuery("SELECT .* FROM `mailboxes`.*WHERE domain_id = \\?.*ORDER BY").
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("mb1", "dom1", "alice", "alice@example.com", "h1", uint64(1<<30), false, uint64(0), nil, now, now).
			AddRow("mb2", "dom1", "bob", "bob@example.com", "h2", uint64(1<<30), false, uint64(0), nil, now, now),
		)

	rows, total, err := repo.ListByDomainID(context.Background(), "dom1", ListOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	require.Equal(t, "alice@example.com", rows[0].EmailCached)
	require.Equal(t, "bob@example.com", rows[1].EmailCached)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestMailboxRepository_ListByDomainID_ExcludeSystem verifies that the
// GH #1056 opt filters the JAB-230 relay out of BOTH the count and the row
// query — otherwise the paginated list would show N-1 rows against a total of
// N and the relay would still surface on the last page.
func TestMailboxRepository_ListByDomainID_ExcludeSystem(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	cols := []string{"id", "domain_id", "local_part", "email_cached", "password_hash",
		"quota_bytes", "is_disabled", "last_usage_bytes", "last_usage_at", "created_at", "updated_at"}
	now := time.Now()

	// Both the count and the select must carry the `system = 0` predicate.
	mock.ExpectQuery("SELECT count.* FROM `mailboxes`.*WHERE domain_id = \\? AND system = 0").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
	mock.ExpectQuery("SELECT .* FROM `mailboxes`.*WHERE domain_id = \\? AND system = 0.*ORDER BY").
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("mb1", "dom1", "alice", "alice@example.com", "h1", uint64(1<<30), false, uint64(0), nil, now, now),
		)

	rows, total, err := repo.ListByDomainID(context.Background(), "dom1", ListOptions{ExcludeSystem: true})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	require.Equal(t, "alice@example.com", rows[0].EmailCached)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_ExistsByDomainAndLocalPart(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectQuery("SELECT count.* FROM `mailboxes` WHERE domain_id = \\? AND local_part = \\?").
		WithArgs("dom1", "alice").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))

	exists, err := repo.ExistsByDomainAndLocalPart(context.Background(), "dom1", "alice")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxRepository_UpdateUsage(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewMailboxRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `mailboxes` SET .* WHERE id = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	now := time.Now()
	err := repo.UpdateUsage(context.Background(), "mb1", 1024*1024, now)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// JAB-147: FindByIDs must batch-load in a single IN query (the N+1-free path
// for the mail list handlers), not one SELECT per id.
func TestMailboxFindByIDs_SingleINQuery(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewMailboxRepository(db)

	cols := []string{"id", "domain_id", "local_part", "email_cached"}
	mock.ExpectQuery("SELECT .* FROM `mailboxes` WHERE id IN \\(\\?,\\?\\)").
		WithArgs("mb1", "mb2").
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("mb1", "dom1", "alice", "alice@example.com").
			AddRow("mb2", "dom1", "bob", "bob@example.com"))

	rows, err := repo.FindByIDs(context.Background(), []string{"mb1", "mb2"})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailboxFindByIDs_EmptyNoQuery(t *testing.T) {
	db, _, raw := newMockDB(t)
	defer raw.Close()
	repo := NewMailboxRepository(db)
	// No ExpectQuery registered — an empty id set must not hit the DB.
	rows, err := repo.FindByIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, rows)
}
