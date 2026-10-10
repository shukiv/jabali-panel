package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A mailbox's app passwords on the mail server are only as old as its
// password: every write of a new password records when it happened.

func TestMailboxPasswordWrites_StampPasswordChangedAt(t *testing.T) {
	for name, write := range map[string]func(MailboxRepository) error{
		"hash": func(r MailboxRepository) error {
			return r.UpdatePasswordHash(context.Background(), "mb1", "$2b$12$new")
		},
		"hash and enc": func(r MailboxRepository) error {
			return r.UpdatePasswordHashAndEnc(context.Background(), "mb1", "$2b$12$new", []byte("sealed"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, mock, raw := newMockDB(t)
			defer raw.Close()
			mock.ExpectBegin()
			mock.ExpectExec("UPDATE `mailboxes` SET .*`password_changed_at`=\\?.* WHERE id = \\?").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			require.NoError(t, write(NewMailboxRepository(db)))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestMailboxCreate_StampsPasswordChangedAt(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `mailboxes` .*`password_changed_at`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	before := time.Now().UTC().Add(-time.Second)
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "bob", PasswordHash: "$2b$12$hash"}
	require.NoError(t, NewMailboxRepository(db).Create(context.Background(), mb))
	require.NotNil(t, mb.PasswordChangedAt)
	require.True(t, mb.PasswordChangedAt.After(before), "stamped %v", mb.PasswordChangedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A mailbox that comes with its own time (a restore) keeps it.
func TestMailboxCreate_KeepsAGivenPasswordChangedAt(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `mailboxes`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	given := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "bob", PasswordHash: "h", PasswordChangedAt: &given}
	require.NoError(t, NewMailboxRepository(db).Create(context.Background(), mb))
	require.Equal(t, given, *mb.PasswordChangedAt)
}

// The mailboxes that may sign in are the ones the mail server's queryLogin
// lets in: enabled, on a verified domain, whose owner isn't suspended. Each
// comes with when its password last changed, or when it was created.
func TestMailLogins_ListsTheMailboxesThatMaySignIn(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	changed := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT m.email_cached AS address, COALESCE\\(m.password_changed_at, m.created_at\\) AS changed_at " +
		"FROM mailboxes m JOIN domains d ON d.id = m.domain_id " +
		"WHERE m.is_disabled = 0 AND d.ownership_status = 'verified' " +
		"AND NOT EXISTS \\(SELECT 1 FROM users u WHERE u.id = d.user_id AND u.suspended = 1\\)").
		WillReturnRows(sqlmock.NewRows([]string{"address", "changed_at"}).
			AddRow("Alice@Example.com", changed).
			AddRow("bob@example.com", created))

	got, err := NewMailLoginRepository(db).ListMailLogins(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{"alice@example.com": changed, "bob@example.com": created}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
