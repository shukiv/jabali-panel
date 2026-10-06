package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestDatabaseUserRepository_FindByID_Found verifies database user retrieval by ID
func TestDatabaseUserRepository_FindByID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_users` WHERE id = \\?.*LIMIT").
		WithArgs("duser_abc123", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "username", "password_hash", "created_at", "updated_at"},
		).AddRow("duser_abc123", "user1", "dbuser1", "hashed_pw", time.Now(), time.Now()))

	du, err := repo.FindByID(context.Background(), "duser_abc123")
	require.NoError(t, err)
	require.NotNil(t, du)
	require.Equal(t, "duser_abc123", du.ID)
	require.Equal(t, "user1", du.UserID)
	require.Equal(t, "dbuser1", du.Username)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_FindByID_NotFound verifies ErrNotFound when database user doesn't exist
func TestDatabaseUserRepository_FindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_users` WHERE id = \\?.*LIMIT").
		WithArgs("duser_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "username", "password_hash", "created_at", "updated_at"},
		))

	du, err := repo.FindByID(context.Background(), "duser_nonexistent")
	require.Error(t, err)
	require.Nil(t, du)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_List verifies listing all database users with search and pagination
func TestDatabaseUserRepository_List(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	// Count query first
	mock.ExpectQuery("SELECT count.* FROM `database_users`").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))

	// Main query
	mock.ExpectQuery("SELECT .* FROM `database_users`.*ORDER BY").
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "username", "password_hash", "created_at", "updated_at"},
		).AddRow("duser1", "user1", "dbuser1", "hash1", time.Now(), time.Now()).
			AddRow("duser2", "user1", "dbuser2", "hash2", time.Now(), time.Now()))

	users, total, err := repo.List(context.Background(), ListOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, users, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_ListByUserID verifies listing database users for a specific user
func TestDatabaseUserRepository_ListByUserID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	// Count query
	mock.ExpectQuery("SELECT count.* FROM `database_users` WHERE user_id = \\?").
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))

	// Main query
	mock.ExpectQuery("SELECT .* FROM `database_users` WHERE user_id = \\?.*ORDER BY").
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "username", "password_hash", "created_at", "updated_at"},
		).AddRow("duser1", "user1", "dbuser1", "hash1", time.Now(), time.Now()).
			AddRow("duser2", "user1", "dbuser2", "hash2", time.Now(), time.Now()))

	users, total, err := repo.ListByUserID(context.Background(), "user1", ListOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, users, 2)
	require.Equal(t, "user1", users[0].UserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_CountByUserID verifies count of database users for a user
func TestDatabaseUserRepository_CountByUserID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `database_users` WHERE user_id = ?")).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(3))

	count, err := repo.CountByUserID(context.Background(), "user1")
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_Create verifies database user creation
func TestDatabaseUserRepository_Create(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	du := &models.DatabaseUser{
		ID:           "duser_new",
		UserID:       "user1",
		Username:     "newdbuser",
		PasswordHash: "newhash",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `database_users`")).
		WithArgs(
			sqlmock.AnyArg(), // id (uuid)
			du.UserID,
			du.Username,
			sqlmock.AnyArg(), // engine (M37 — defaults to mariadb)
			du.PasswordHash,
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), du)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_Create_DuplicateIsConflict: a duplicate id or
// (user, username) surfaces as ErrConflict, like the other repositories, so
// callers (the create handler's 409, a backup restore's "already there") can
// tell it from a real failure (GH #1993).
func TestDatabaseUserRepository_Create_DuplicateIsConflict(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `database_users`")).
		WillReturnError(&mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry 'user1-alice_db' for key 'uniq_user_username'"})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), &models.DatabaseUser{ID: "duser_dup", UserID: "user1", Username: "alice_db", PasswordHash: "h"})
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_Delete verifies database user deletion
func TestDatabaseUserRepository_Delete(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `database_users` WHERE id = ?")).
		WithArgs("duser_abc123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "duser_abc123")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_UpdatePasswordHash verifies password hash update
func TestDatabaseUserRepository_UpdatePasswordHash(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	newHash := "updated_hash"

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `database_users` SET `password_hash`=")).
		WithArgs(newHash, sqlmock.AnyArg(), "duser_abc123"). // updated_at is auto-set by GORM
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdatePasswordHash(context.Background(), "duser_abc123", newHash)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_ExistsByUserAndUsername_Exists verifies checking if username exists for user
func TestDatabaseUserRepository_ExistsByUserAndUsername_Exists(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `database_users` WHERE user_id = ? AND username = ?")).
		WithArgs("user1", "dbuser1").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))

	exists, err := repo.ExistsByUserAndUsername(context.Background(), "user1", "dbuser1")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserRepository_ExistsByUserAndUsername_NotExists verifies username not found
func TestDatabaseUserRepository_ExistsByUserAndUsername_NotExists(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `database_users` WHERE user_id = ? AND username = ?")).
		WithArgs("user1", "nonexistent").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))

	exists, err := repo.ExistsByUserAndUsername(context.Background(), "user1", "nonexistent")
	require.NoError(t, err)
	require.False(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}
