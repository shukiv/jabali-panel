package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func testLogStream() *models.LogAccessStream {
	return &models.LogAccessStream{
		ID:        "01HLOGSTREAM00000000000000",
		UserID:    "01HUSER000000000000000000A",
		LogType:   "access",
		StreamKey: "0123456789abcdef0123456789abcdef",
		ExpiresAt: time.Now().Add(15 * time.Minute),
		CreatedAt: time.Now(),
	}
}

// JAB-347: ReserveWithinCap locks the beneficiary's users row FOR UPDATE, counts
// active streams, and inserts — all in one transaction — so concurrent creates
// serialize on the row lock and cannot exceed the cap. These pin the query
// STRUCTURE (lock + count + conditional insert); the row lock is the real
// serialization guarantee (same pattern proven for FTP in JAB-262).
func TestLogStreamReserveWithinCap_UnderCapInserts(t *testing.T) {
	db, mock, raw := newMockFtpDB(t) // generic sqlmock+gorm harness (in-package)
	defer raw.Close()
	repo := NewLogAccessStreamRepository(db)
	s := testLogStream()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(s.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(s.UserID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `log_access_streams` WHERE user_id = ? AND expires_at > ?")).
		WithArgs(s.UserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectExec("INSERT INTO `log_access_streams`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.ReserveWithinCap(context.Background(), s, 5))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLogStreamReserveWithinCap_AtCapRejectsNoInsert(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewLogAccessStreamRepository(db)
	s := testLogStream()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(s.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(s.UserID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `log_access_streams` WHERE user_id = ? AND expires_at > ?")).
		WithArgs(s.UserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectRollback()

	err := repo.ReserveWithinCap(context.Background(), s, 5)
	require.ErrorIs(t, err, ErrLogStreamCapExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A missing beneficiary row (the FOR UPDATE lock finds nothing) must not insert.
func TestLogStreamReserveWithinCap_UserMissing(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewLogAccessStreamRepository(db)
	s := testLogStream()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(s.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // empty
	mock.ExpectRollback()

	err := repo.ReserveWithinCap(context.Background(), s, 5)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
