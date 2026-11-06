package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// CreateWithinCap locks the owner's users row FOR UPDATE, counts the owner's
// domains and inserts, all in one transaction, so two creates racing for the
// last free slot serialize on the row lock (the FTP and log-stream caps use the
// same shape). These pin that structure.
func TestDomainCreateWithinCap_UnderCapInserts(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)
	d := &models.Domain{ID: "d1", UserID: "u1", Name: "a.test"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `domains` WHERE user_id = ?")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec("INSERT INTO `domains`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateWithinCap(context.Background(), d, 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainCreateWithinCap_AtCapRejectsNoInsert(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)
	d := &models.Domain{ID: "d1", UserID: "u1", Name: "a.test"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `domains` WHERE user_id = ?")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectRollback()

	err := repo.CreateWithinCap(context.Background(), d, 3)
	require.ErrorIs(t, err, ErrDomainCapExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainCreateWithinCap_OwnerMissing(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	err := repo.CreateWithinCap(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "a.test"}, 3)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainCreateWithinCap_DuplicateNameIsConflict(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `domains` WHERE user_id = ?")).
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO `domains`").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'a.test' for key 'name'"})
	mock.ExpectRollback()

	err := repo.CreateWithinCap(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "a.test"}, 3)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
