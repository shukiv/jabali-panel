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

// TestPHPPoolRepository_Create verifies pool creation
func TestPHPPoolRepository_Create(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	pool := &models.PHPPool{
		ID:                        "pool_abc123",
		UserID:                    "user1",
		PHPVersion:                "8.3",
		PmMode:                    "ondemand",
		PmMaxChildren:             20,
		ProcessIdleTimeoutSeconds: 60,
		Status:                    "pending",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `php_pools`")).
		WithArgs(
			pool.ID,
			pool.UserID,
			pool.PHPVersion,
			pool.PmMode,
			pool.PmMaxChildren,
			pool.ProcessIdleTimeoutSeconds,
			sqlmock.AnyArg(), // pm_start_servers
			sqlmock.AnyArg(), // pm_min_spare_servers
			sqlmock.AnyArg(), // pm_max_spare_servers
			sqlmock.AnyArg(), // pm_max_requests
			sqlmock.AnyArg(), // request_terminate_timeout_seconds
			sqlmock.AnyArg(), // slowlog_timeout_seconds (GH #1332 item 12)
			sqlmock.AnyArg(), // performance_mode
			pool.Status,
			sqlmock.AnyArg(), // last_error
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), pool)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_FindByID_Found verifies pool retrieval by ID
func TestPHPPoolRepository_FindByID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	mock.ExpectQuery("SELECT .* FROM `php_pools` WHERE id = \\?.*LIMIT").
		WithArgs("pool_abc123", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "php_version", "pm_mode", "pm_max_children", "process_idle_timeout_seconds", "status", "last_error", "created_at", "updated_at"},
		).AddRow("pool_abc123", "user1", "8.3", "ondemand", 20, 60, "active", nil, time.Now(), time.Now()))

	pool, err := repo.FindByID(context.Background(), "pool_abc123")
	require.NoError(t, err)
	require.NotNil(t, pool)
	require.Equal(t, "pool_abc123", pool.ID)
	require.Equal(t, "user1", pool.UserID)
	require.Equal(t, "8.3", pool.PHPVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_FindByID_NotFound verifies ErrNotFound when pool doesn't exist
func TestPHPPoolRepository_FindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	mock.ExpectQuery("SELECT .* FROM `php_pools` WHERE id = \\?.*LIMIT").
		WithArgs("pool_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "php_version", "pm_mode", "pm_max_children", "process_idle_timeout_seconds", "status", "last_error", "created_at", "updated_at"},
		))

	pool, err := repo.FindByID(context.Background(), "pool_nonexistent")
	require.Error(t, err)
	require.Nil(t, pool)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_FindByUserID_Found verifies pool retrieval by user ID
func TestPHPPoolRepository_FindByUserID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	// JAB-388: the default pool is the earliest row — the lookup must ORDER BY
	// created_at ASC, id ASC, identical to ListByUserID, so both paths agree on
	// pools[0]=default (the id ASC tiebreak keeps the JAB-174 anti-duplicate
	// determinism on a created_at tie).
	mock.ExpectQuery("SELECT .* FROM `php_pools` WHERE user_id = \\?.*ORDER BY created_at ASC, id ASC.*LIMIT").
		WithArgs("user1", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "php_version", "pm_mode", "pm_max_children", "process_idle_timeout_seconds", "status", "last_error", "created_at", "updated_at"},
		).AddRow("pool_abc123", "user1", "8.3", "ondemand", 20, 60, "active", nil, time.Now(), time.Now()))

	pool, err := repo.FindByUserID(context.Background(), "user1")
	require.NoError(t, err)
	require.NotNil(t, pool)
	require.Equal(t, "user1", pool.UserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_FindByUserID_NotFound verifies ErrNotFound when no pool exists for user
func TestPHPPoolRepository_FindByUserID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	mock.ExpectQuery("SELECT .* FROM `php_pools` WHERE user_id = \\?.*LIMIT").
		WithArgs("user_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "php_version", "pm_mode", "pm_max_children", "process_idle_timeout_seconds", "status", "last_error", "created_at", "updated_at"},
		))

	pool, err := repo.FindByUserID(context.Background(), "user_nonexistent")
	require.Error(t, err)
	require.Nil(t, pool)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_ListAll verifies list with pagination
func TestPHPPoolRepository_ListAll(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	// Mock COUNT query. GORM emits `count(*)` lowercase.
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `php_pools`").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	// Mock SELECT query with LIMIT/OFFSET. GORM elides the OFFSET clause
	// when Offset is zero; use a non-zero offset so both args bind.
	mock.ExpectQuery("SELECT .* FROM `php_pools` ORDER BY created_at DESC LIMIT").
		WithArgs(10, 5).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "php_version", "pm_mode", "pm_max_children", "process_idle_timeout_seconds", "status", "last_error", "created_at", "updated_at"},
		).AddRow("pool1", "user1", "8.3", "ondemand", 20, 60, "active", nil, time.Now(), time.Now()).
			AddRow("pool2", "user2", "8.2", "dynamic", 25, 120, "pending", nil, time.Now(), time.Now()))

	pools, total, err := repo.ListAll(context.Background(), ListOptions{Limit: 10, Offset: 5})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, pools, 2)
	require.Equal(t, "pool1", pools[0].ID)
	require.Equal(t, "pool2", pools[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_Update verifies pool update
func TestPHPPoolRepository_Update(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	pool := &models.PHPPool{
		ID:                        "pool_abc123",
		UserID:                    "user1",
		PHPVersion:                "8.3",
		PmMode:                    "dynamic",
		PmMaxChildren:             25,
		ProcessIdleTimeoutSeconds: 120,
		Status:                    "active",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `php_pools` SET")).
		WithArgs(
			sqlmock.AnyArg(), // user_id
			sqlmock.AnyArg(), // php_version
			sqlmock.AnyArg(), // pm_mode
			sqlmock.AnyArg(), // pm_max_children
			sqlmock.AnyArg(), // process_idle_timeout_seconds
			sqlmock.AnyArg(), // pm_start_servers
			sqlmock.AnyArg(), // pm_min_spare_servers
			sqlmock.AnyArg(), // pm_max_spare_servers
			sqlmock.AnyArg(), // pm_max_requests
			sqlmock.AnyArg(), // request_terminate_timeout_seconds
			sqlmock.AnyArg(), // slowlog_timeout_seconds (GH #1332 item 12)
			sqlmock.AnyArg(), // performance_mode
			sqlmock.AnyArg(), // status
			sqlmock.AnyArg(), // last_error
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
			"pool_abc123",    // id
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Update(context.Background(), pool)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_Delete verifies pool deletion
func TestPHPPoolRepository_Delete(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `php_pools` WHERE id = ?")).
		WithArgs("pool_abc123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "pool_abc123")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPHPPoolRepository_SetStatus verifies status update
func TestPHPPoolRepository_SetStatus(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewPHPPoolRepository(db)

	errMsg := "failed to apply pool"

	// GORM emits UPDATE with columns in alphabetical order:
	// `last_error`, `status`, `updated_at`. Match the prefix loosely
	// via regex; bind order matches the field order GORM writes.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `php_pools` SET .* WHERE id = ?").
		WithArgs(
			&errMsg,
			"error",
			sqlmock.AnyArg(), // updated_at
			"pool_abc123",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.SetStatus(context.Background(), "pool_abc123", "error", &errMsg)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
