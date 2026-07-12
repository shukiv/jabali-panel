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

func newMockForwarderDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
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

// JAB-107: ListByUserID must scope to the owner in SQL (JOIN domains on
// user_id) rather than pull a global window and filter in memory — otherwise a
// tenant's rows past the global newest-N vanish. The mock asserts both the
// count and the select carry the join + user filter, and that mailbox_id
// IS NOT NULL is applied so `total` matches the rendered set.
func TestEmailForwarder_ListByUserID_ScopesInSQL(t *testing.T) {
	db, mock, raw := newMockForwarderDB(t)
	defer raw.Close()
	repo := NewEmailForwarderRepository(db)

	// Count: joined + user-scoped + mailbox-keyed.
	mock.ExpectQuery(`SELECT count\(\*\) FROM .email_forwarders. JOIN domains ON domains\.id = email_forwarders\.domain_id WHERE domains\.user_id = \? AND email_forwarders\.mailbox_id IS NOT NULL`).
		WithArgs("user-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(742))

	// Select: same scope, paginated, ordered.
	mbID := "mbx-1"
	mock.ExpectQuery(`SELECT email_forwarders\.\* FROM .email_forwarders. JOIN domains ON domains\.id = email_forwarders\.domain_id WHERE domains\.user_id = \? AND email_forwarders\.mailbox_id IS NOT NULL ORDER BY email_forwarders\.created_at DESC LIMIT`).
		WithArgs("user-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "mailbox_id", "domain_id", "created_at"}).
			AddRow("fwd-1", mbID, "dom-1", time.Now()))

	rows, total, err := repo.ListByUserID(context.Background(), "user-1",
		ListOptions{Offset: 500, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, int64(742), total, "total must be the user's full count, not a capped window")
	require.Len(t, rows, 1)
	require.Equal(t, "fwd-1", rows[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
