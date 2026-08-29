package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)

	// MariaDB version probe GORM sends on open.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).
			AddRow("10.11.0-MariaDB"))

	db, err := gorm.Open(
		mysql.New(mysql.Config{Conn: raw}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	require.NoError(t, err)

	return db, mock, raw
}


// TestCountByUserID verifies correct count for user's databases
func TestCountByUserID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect COUNT query
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `databases` WHERE user_id = ?")).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(3))

	count, err := repo.CountByUserID(context.Background(), "user1")
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFindByID_Found verifies database retrieval
func TestFindByID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect SELECT query (GORM adds ORDER BY and LIMIT for First())
	mock.ExpectQuery("SELECT .* FROM `databases` WHERE id = \\?.*LIMIT").
		WithArgs("db_abc123", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "name", "engine", "charset", "collation", "created_at", "updated_at"},
		).AddRow("db_abc123", "user1", "testdb", "mariadb", "utf8mb4", "utf8mb4_unicode_ci", time.Now(), time.Now()))

	d, err := repo.FindByID(context.Background(), "db_abc123")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, "db_abc123", d.ID)
	require.Equal(t, "user1", d.UserID)
	require.Equal(t, "testdb", d.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFindByID_NotFound verifies ErrNotFound when database doesn't exist
func TestFindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect SELECT query returning no rows (GORM adds ORDER BY and LIMIT for First())
	mock.ExpectQuery("SELECT .* FROM `databases` WHERE id = \\?.*LIMIT").
		WithArgs("db_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "name", "engine", "charset", "collation", "created_at", "updated_at"},
		))

	d, err := repo.FindByID(context.Background(), "db_nonexistent")
	require.Error(t, err)
	require.Nil(t, d)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestListByUserID verifies database listing for a specific user
func TestListByUserID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect COUNT query
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `databases` WHERE user_id = \\?").
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))

	// Expect SELECT query with ORDER BY and LIMIT (added by applyListOptions)
	mock.ExpectQuery("SELECT .* FROM `databases` WHERE user_id = \\?.*LIMIT").
		WithArgs("user1", 20).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "name", "engine", "charset", "collation", "created_at", "updated_at"},
		).
			AddRow("db_abc123", "user1", "testdb1", "mariadb", "utf8mb4", "utf8mb4_unicode_ci", time.Now(), time.Now()).
			AddRow("db_xyz789", "user1", "testdb2", "mariadb", "utf8mb4", "utf8mb4_unicode_ci", time.Now(), time.Now()))

	databases, total, err := repo.ListByUserID(context.Background(), "user1", ListOptions{Limit: 20})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, databases, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCreate_Success verifies database creation
func TestCreate_Success(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	now := time.Now()
	d := &models.Database{
		ID:        "db_abc123",
		UserID:    "user1",
		Name:      "testdb",
		Engine:    "mariadb",
		Charset:   "utf8mb4",
		Collation: "utf8mb4_unicode_ci",
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Expect INSERT query
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `databases`").
		WithArgs(d.ID, d.UserID, d.Name, d.Engine, d.Charset, d.Collation, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), d)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDelete_Success verifies database deletion
func TestDelete_Success(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect DELETE query
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `databases` WHERE id = \\?").
		WithArgs("db_abc123").
		WillReturnResult(sqlmock.NewResult(0, 1)) // 1 row affected
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "db_abc123")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDelete_NotFound verifies ErrNotFound when database doesn't exist
func TestDelete_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect DELETE query with no rows affected
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `databases` WHERE id = \\?").
		WithArgs("db_nonexistent").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "db_nonexistent")
	require.Error(t, err)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestExistsByUserAndName_Exists verifies true when database exists
func TestExistsByUserAndName_Exists(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect COUNT query
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `databases` WHERE user_id = \\? AND name = \\?").
		WithArgs("user1", "testdb").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))

	exists, err := repo.ExistsByUserAndName(context.Background(), "user1", "testdb")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestExistsByUserAndName_NotExists verifies false when database doesn't exist
func TestExistsByUserAndName_NotExists(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDatabaseRepository(db)

	// Expect COUNT query
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `databases` WHERE user_id = \\? AND name = \\?").
		WithArgs("user1", "nonexistent").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))

	exists, err := repo.ExistsByUserAndName(context.Background(), "user1", "nonexistent")
	require.NoError(t, err)
	require.False(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}
