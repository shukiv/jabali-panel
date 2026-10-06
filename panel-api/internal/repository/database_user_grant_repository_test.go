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

// TestDatabaseUserGrantRepository_FindByID_Found verifies grant retrieval by ID
func TestDatabaseUserGrantRepository_FindByID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_user_grants` WHERE id = \\?.*LIMIT").
		WithArgs("grant_abc123", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "database_id", "database_user_id", "grant_level", "created_at", "updated_at"},
		).AddRow("grant_abc123", "db1", "duser1", "rw", time.Now(), time.Now()))

	g, err := repo.FindByID(context.Background(), "grant_abc123")
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, "grant_abc123", g.ID)
	require.Equal(t, "db1", g.DatabaseID)
	require.Equal(t, "duser1", g.DatabaseUserID)
	require.Equal(t, "rw", g.GrantLevel)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_FindByID_NotFound verifies ErrNotFound when grant doesn't exist
func TestDatabaseUserGrantRepository_FindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_user_grants` WHERE id = \\?.*LIMIT").
		WithArgs("grant_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "database_id", "database_user_id", "grant_level", "created_at", "updated_at"},
		))

	g, err := repo.FindByID(context.Background(), "grant_nonexistent")
	require.Error(t, err)
	require.Nil(t, g)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_ListByDatabaseID verifies listing grants for a database
func TestDatabaseUserGrantRepository_ListByDatabaseID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_user_grants` WHERE database_id = \\?").
		WithArgs("db1").
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "database_id", "database_user_id", "grant_level", "created_at", "updated_at"},
		).AddRow("grant1", "db1", "duser1", "rw", time.Now(), time.Now()).
			AddRow("grant2", "db1", "duser2", "ro", time.Now(), time.Now()))

	grants, err := repo.ListByDatabaseID(context.Background(), "db1")
	require.NoError(t, err)
	require.Len(t, grants, 2)
	require.Equal(t, "db1", grants[0].DatabaseID)
	require.Equal(t, "db1", grants[1].DatabaseID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_ListByDatabaseUserID verifies listing grants for a database user
func TestDatabaseUserGrantRepository_ListByDatabaseUserID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_user_grants` WHERE database_user_id = \\?").
		WithArgs("duser1").
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "database_id", "database_user_id", "grant_level", "created_at", "updated_at"},
		).AddRow("grant1", "db1", "duser1", "rw", time.Now(), time.Now()).
			AddRow("grant2", "db2", "duser1", "ro", time.Now(), time.Now()))

	grants, err := repo.ListByDatabaseUserID(context.Background(), "duser1")
	require.NoError(t, err)
	require.Len(t, grants, 2)
	require.Equal(t, "duser1", grants[0].DatabaseUserID)
	require.Equal(t, "duser1", grants[1].DatabaseUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_Create verifies grant creation
func TestDatabaseUserGrantRepository_Create(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	grant := &models.DatabaseUserGrant{
		ID:               "grant_new",
		DatabaseID:       "db1",
		DatabaseUserID:   "duser1",
		GrantLevel:       "rw",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `database_user_grants`")).
		WithArgs(
			sqlmock.AnyArg(), // id (uuid)
			grant.DatabaseID,
			grant.DatabaseUserID,
			grant.GrantLevel,
			sqlmock.AnyArg(), // privileges (defaults to 'ALL' via GORM)
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), grant)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_Create_DuplicateIsConflict: a grant that is
// already there (same id, or the same database and user) surfaces as
// ErrConflict so a backup restore skips it instead of reporting a failure
// (GH #1993).
func TestDatabaseUserGrantRepository_Create_DuplicateIsConflict(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `database_user_grants`")).
		WillReturnError(&mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry 'db1-duser1' for key 'uniq_db_user'"})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), &models.DatabaseUserGrant{ID: "grant_dup", DatabaseID: "db1", DatabaseUserID: "duser1", GrantLevel: "rw"})
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_Delete verifies grant deletion
func TestDatabaseUserGrantRepository_Delete(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `database_user_grants` WHERE id = ?")).
		WithArgs("grant_abc123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "grant_abc123")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_UpdateLevel verifies grant level update
func TestDatabaseUserGrantRepository_UpdateLevel(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `database_user_grants` SET `grant_level`=")).
		WithArgs("ro", sqlmock.AnyArg(), "grant_abc123"). // updated_at is auto-set by GORM
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdateLevel(context.Background(), "grant_abc123", "ro")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_FindByDBAndDBUser_Found verifies finding grant by database and user IDs
func TestDatabaseUserGrantRepository_FindByDBAndDBUser_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_user_grants` WHERE database_id = \\? AND database_user_id = \\?.*LIMIT").
		WithArgs("db1", "duser1", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "database_id", "database_user_id", "grant_level", "created_at", "updated_at"},
		).AddRow("grant_abc123", "db1", "duser1", "rw", time.Now(), time.Now()))

	g, err := repo.FindByDBAndDBUser(context.Background(), "db1", "duser1")
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, "grant_abc123", g.ID)
	require.Equal(t, "db1", g.DatabaseID)
	require.Equal(t, "duser1", g.DatabaseUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDatabaseUserGrantRepository_FindByDBAndDBUser_NotFound verifies ErrNotFound when grant doesn't exist
func TestDatabaseUserGrantRepository_FindByDBAndDBUser_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseUserGrantRepository(db)

	mock.ExpectQuery("SELECT .* FROM `database_user_grants` WHERE database_id = \\? AND database_user_id = \\?.*LIMIT").
		WithArgs("db1", "duser_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "database_id", "database_user_id", "grant_level", "created_at", "updated_at"},
		))

	g, err := repo.FindByDBAndDBUser(context.Background(), "db1", "duser_nonexistent")
	require.Error(t, err)
	require.Nil(t, g)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
