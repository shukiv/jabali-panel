package repository

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// A new mailbox has no earlier password whose login a credential could have
// been made with: its time stays unset, and the sweep goes by created_at.
func TestMailboxCreate_LeavesPasswordChangedAtUnset(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `mailboxes`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "bob", PasswordHash: "$2b$12$hash"}
	require.NoError(t, NewMailboxRepository(db).Create(context.Background(), mb))
	require.Nil(t, mb.PasswordChangedAt)
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
// comes with the cutoff for its app passwords: a minute after its password
// last changed (the mail server still takes the old password from its login
// cache until the panel's flush reaches it, so an app password made in that
// window may have been made with the old one), or when it was created.
func TestMailLogins_ListsTheMailboxesThatMaySignIn(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	changed := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT m.email_cached AS address, m.password_changed_at AS changed_at, m.created_at AS created_at " +
		"FROM mailboxes m JOIN domains d ON d.id = m.domain_id " +
		"WHERE m.is_disabled = 0 AND d.ownership_status = 'verified' " +
		"AND NOT EXISTS \\(SELECT 1 FROM users u WHERE u.id = d.user_id AND u.suspended = 1\\)").
		WillReturnRows(sqlmock.NewRows([]string{"address", "changed_at", "created_at"}).
			AddRow("Alice@Example.com", changed, created).
			AddRow("bob@example.com", nil, created))

	got, err := NewMailLoginRepository(db).ListMailLogins(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{
		"alice@example.com": changed.Add(PasswordChangeGrace),
		"bob@example.com":   created,
	}, got)
	require.Equal(t, time.Minute, PasswordChangeGrace)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Stalwart signs in an app password without asking the SQL directory, so the
// sweep's list of mailboxes that may sign in must be exactly the directory's
// login query (install.sh's converger carries it): a mailbox the directory
// refuses must lose its app passwords. The two WHERE clauses agree, but for
// the directory's lookup of the one address.
func TestMailLoginQuery_MatchesTheMailServerLoginQuery(t *testing.T) {
	dir, err := os.Getwd()
	require.NoError(t, err)
	var raw []byte
	for {
		if raw, err = os.ReadFile(filepath.Join(dir, "install.sh")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "install.sh not found above the package")
		dir = parent
	}
	m := regexp.MustCompile(`local query_login="([^"]*)"`).FindSubmatch(raw)
	require.NotNil(t, m, "install.sh: no local query_login")

	_, login, ok := strings.Cut(string(m[1]), " WHERE m.email_cached = ? AND ")
	require.True(t, ok, "queryLogin no longer looks up m.email_cached first: %s", m[1])
	_, ours, ok := strings.Cut(mailLoginQuery, " WHERE ")
	require.True(t, ok)
	require.Equal(t, login, ours)
}
