package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// JAB-370 AC4: no password hash or encrypted password material is selected into
// inventory rows. The per-domain mailbox list (GET /domains/:id/mailboxes, the
// tenant Mailboxes tab drill-down, `jabali mailbox list`, disk usage, purge, and
// backup selection) is served by ListByDomainID. This test records the SQL the
// row query actually sends, so it fails on a `SELECT *` that pulls the bcrypt
// password_hash and the AES password_enc into memory.
func TestMailboxRepository_ListByDomainID_SelectsNoSecretColumns(t *testing.T) {
	var sent []string
	matcher := sqlmock.QueryMatcherFunc(func(_, actual string) error {
		sent = append(sent, actual)
		return nil
	})
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	defer raw.Close()

	mock.ExpectQuery("version").
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: raw}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	now := time.Now()
	mock.ExpectQuery("count").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
	mock.ExpectQuery("rows").
		WillReturnRows(sqlmock.NewRows([]string{"id", "domain_id", "local_part", "email_cached",
			"quota_bytes", "is_disabled", "last_usage_bytes", "last_usage_at", "created_at", "updated_at"}).
			AddRow("mb1", "dom1", "alice", "alice@example.com", uint64(1<<30), false, uint64(7), nil, now, now))

	rows, total, err := NewMailboxRepository(db).ListByDomainID(context.Background(), "dom1",
		ListOptions{ExcludeSystem: true, Search: "ali", Sort: "email", Order: "asc", Limit: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	require.Equal(t, "alice@example.com", rows[0].EmailCached)
	require.NoError(t, mock.ExpectationsWereMet())

	var rowQueries []string
	for _, q := range sent {
		if strings.Contains(q, "FROM `mailboxes`") && !strings.Contains(strings.ToLower(q), "count(") {
			rowQueries = append(rowQueries, q)
		}
	}
	require.Len(t, rowQueries, 1, "want exactly one row query, sent %q", sent)
	q := rowQueries[0]
	t.Logf("row query: %s", q)
	if regexp.MustCompile(`SELECT\s+\*`).MatchString(q) {
		t.Fatalf("the per-domain mailbox list must select an explicit column list, not *: %s", q)
	}
	for _, secret := range []string{"password_hash", "password_enc"} {
		if strings.Contains(q, secret) {
			t.Fatalf("the per-domain mailbox list must not select %s: %s", secret, q)
		}
	}
	for _, col := range []string{"email_cached", "last_usage_bytes", "is_disabled", "send_only", "system"} {
		if !strings.Contains(q, col) {
			t.Errorf("the per-domain mailbox list must still select %s: %s", col, q)
		}
	}
}
