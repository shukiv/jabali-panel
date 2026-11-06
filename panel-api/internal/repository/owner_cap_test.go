package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CreateWithinCap locks the owner's users row FOR UPDATE, counts the owner's
// rows and inserts, all in one transaction, so two creates racing for the
// last free slot serialize on the row lock. These pin that structure.

func expectOwnerLockAndCount(mock sqlmock.Sqlmock, table string, count int) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `" + table + "` WHERE user_id = ?")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func TestDatabaseUserCreateWithinCap_UnderCapInserts(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	expectOwnerLockAndCount(mock, "database_users", 2)
	mock.ExpectExec("INSERT INTO `database_users`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	du := &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_app", Engine: "mariadb"}
	require.NoError(t, NewDatabaseUserRepository(db).CreateWithinCap(context.Background(), du, 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDatabaseUserCreateWithinCap_AtCapRejectsNoInsert(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	expectOwnerLockAndCount(mock, "database_users", 3)
	mock.ExpectRollback()

	du := &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_app", Engine: "mariadb"}
	err := NewDatabaseUserRepository(db).CreateWithinCap(context.Background(), du, 3)
	require.ErrorIs(t, err, ErrDatabaseUserCapExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDatabaseUserCreateWithinCap_OwnerMissing(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	du := &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_app", Engine: "mariadb"}
	err := NewDatabaseUserRepository(db).CreateWithinCap(context.Background(), du, 3)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDatabaseCreateWithinCap_UnderCapInserts(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	expectOwnerLockAndCount(mock, "databases", 1)
	mock.ExpectExec("INSERT INTO `databases`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	d := &models.Database{ID: "db1", UserID: "u1", Name: "alice_shop", Engine: "mariadb"}
	require.NoError(t, NewDatabaseRepository(db).CreateWithinCap(context.Background(), d, 2))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDatabaseCreateWithinCap_AtCapRejectsNoInsert(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	expectOwnerLockAndCount(mock, "databases", 2)
	mock.ExpectRollback()

	d := &models.Database{ID: "db1", UserID: "u1", Name: "alice_shop", Engine: "mariadb"}
	err := NewDatabaseRepository(db).CreateWithinCap(context.Background(), d, 2)
	require.ErrorIs(t, err, ErrDatabaseCapExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}
