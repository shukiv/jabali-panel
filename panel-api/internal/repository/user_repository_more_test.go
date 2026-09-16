package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestUserRepository_FindByID(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT \* FROM .users. WHERE id = \?`).
		WithArgs("01HRCWR7CKMCBEDF2PYQ7G0D2J", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "email", "name_first", "name_last", "password_hash",
			"is_admin", "linux_uid", "created_at", "updated_at",
		}).AddRow(
			"01HRCWR7CKMCBEDF2PYQ7G0D2J", "alice@example.com", "A", "B",
			"hash", false, nil, now, now,
		))

	u, err := repo.FindByID(context.Background(), "01HRCWR7CKMCBEDF2PYQ7G0D2J")
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", u.Email)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_List(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT count\(\*\) FROM .users.`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT \* FROM .users. ORDER BY created_at DESC LIMIT \?`).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "email", "name_first", "name_last", "password_hash",
			"is_admin", "linux_uid", "created_at", "updated_at",
		}).
			AddRow("01HRCWR7CKMCBEDF2PYQ7G0D2J", "a@x", "", "", "h", false, nil, now, now).
			AddRow("01HRCWR7CKMCBEDF2PYQ7G0D2K", "b@x", "", "", "h", false, nil, now, now))

	out, total, err := repo.List(context.Background(), repository.ListOptions{Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, out, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_Update(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	u := &models.User{
		ID:        "01HRCWR7CKMCBEDF2PYQ7G0D2J",
		Email:     "new@example.com",
		NameFirst: "Alice",
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .users. SET`).
		WithArgs(
			u.Email, u.NameFirst, u.NameLast, u.PasswordHash, u.PackageID, u.LinuxUID,
			sqlmock.AnyArg(), // updated_at
			u.ID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Update(context.Background(), u)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_Delete_HardDeletes(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM .users. WHERE`).
		WithArgs("01HRCWR7CKMCBEDF2PYQ7G0D2J").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "01HRCWR7CKMCBEDF2PYQ7G0D2J")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM .users. WHERE`).
		WithArgs("missing-id").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "missing-id")
	require.Error(t, err)
	assert.ErrorIs(t, err, repository.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_Create_DuplicateEmail(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	u := &models.User{
		ID:           "01HRCWR7CKMCBEDF2PYQ7G0D2J",
		Email:        "dup@example.com",
		PasswordHash: "hash",
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO .users.`).
		WillReturnError(&mysqldriver.MySQLError{
			Number:  1062,
			Message: "Duplicate entry 'dup@example.com' for key 'ux_users_email'",
		})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), u)
	require.Error(t, err)
	assert.True(t, errors.Is(err, repository.ErrConflict), "expected ErrConflict, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}
