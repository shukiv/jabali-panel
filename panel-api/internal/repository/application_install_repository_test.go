package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestWordPressInstallCreate_Success(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	install := &models.WordPressInstall{
		ID:            "inst_abc123",
		UserID:        "user1",
		DomainID:      "domain1",
		DBID:          models.DBIDPtr("db1"),
		AdminUsername: "admin",
		AdminEmail:    "admin@example.com",
		Locale:        "en_US",
		UseWWW:        false,
		Subdirectory:  "",
		Status:        "pending",
		LastError:     "",
		CreatedAt:     now,
		UpdatedAt:     now,
		AppType:       "wordpress",
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `application_installs`").
		WithArgs(
			install.ID, install.UserID, install.DomainID, install.DBID,
			install.Version, install.AdminUsername, install.AdminEmail,
			install.Locale, install.UseWWW, install.Subdirectory, install.Status, install.LastError,
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			install.AppType,
			sqlmock.AnyArg(), // cache_enabled (GH #406)
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), install)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallCreate_UniqueDomainIDConstraint(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	install := &models.WordPressInstall{
		ID:            "inst_abc123",
		UserID:        "user1",
		DomainID:      "domain1",
		DBID:          models.DBIDPtr("db1"),
		AdminUsername: "admin",
		AdminEmail:    "admin@example.com",
		Locale:        "en_US",
		UseWWW:        false,
		Subdirectory:  "",
		Status:        "pending",
		LastError:     "",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	mock.ExpectBegin()
	// 15 columns after M16 rollback (migration 000051 dropped
	// oidc_client_id + oidc_client_secret_enc).
	mock.ExpectExec("INSERT INTO `application_installs`").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()). // +cache_enabled (GH #406)
		WillReturnError(sql.ErrNoRows) // Simulates UNIQUE constraint violation
	mock.ExpectRollback()

	err := repo.Create(context.Background(), install)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallFindByID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE id = \\?.*LIMIT").
		WithArgs("inst_abc123", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		).AddRow(
			"inst_abc123", "user1", "domain1", "db1",
			"6.5.3", "admin", "admin@example.com", "en_US", false, "", "ready", "", now, now,
		))

	install, err := repo.FindByID(context.Background(), "inst_abc123")
	require.NoError(t, err)
	require.NotNil(t, install)
	require.Equal(t, "inst_abc123", install.ID)
	require.Equal(t, "user1", install.UserID)
	require.Equal(t, "domain1", install.DomainID)
	require.Equal(t, "ready", install.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallFindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE id = \\?.*LIMIT").
		WithArgs("inst_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		))

	install, err := repo.FindByID(context.Background(), "inst_nonexistent")
	require.Error(t, err)
	require.Nil(t, install)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallFindByIDAndUserID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE id = \\? AND user_id = \\?.*LIMIT").
		WithArgs("inst_abc123", "user1", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		).AddRow(
			"inst_abc123", "user1", "domain1", "db1",
			"6.5.3", "admin", "admin@example.com", "en_US", false, "", "ready", "", now, now,
		))

	install, err := repo.FindByIDAndUserID(context.Background(), "inst_abc123", "user1")
	require.NoError(t, err)
	require.NotNil(t, install)
	require.Equal(t, "inst_abc123", install.ID)
	require.Equal(t, "user1", install.UserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallFindByIDAndUserID_DifferentUser(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	// Install exists but belongs to a different user; should return ErrNotFound
	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE id = \\? AND user_id = \\?.*LIMIT").
		WithArgs("inst_abc123", "user2", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		))

	install, err := repo.FindByIDAndUserID(context.Background(), "inst_abc123", "user2")
	require.Error(t, err)
	require.Nil(t, install)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallFindByDomainID_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE domain_id = \\?.*LIMIT").
		WithArgs("domain1", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		).AddRow(
			"inst_abc123", "user1", "domain1", "db1",
			"6.5.3", "admin", "admin@example.com", "en_US", false, "", "ready", "", now, now,
		))

	install, err := repo.FindByDomainID(context.Background(), "domain1")
	require.NoError(t, err)
	require.NotNil(t, install)
	require.Equal(t, "domain1", install.DomainID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallFindByDomainID_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE domain_id = \\?.*LIMIT").
		WithArgs("domain_nonexistent", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		))

	install, err := repo.FindByDomainID(context.Background(), "domain_nonexistent")
	require.Error(t, err)
	require.Nil(t, install)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallListByUserID(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	// Expect COUNT query
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `application_installs` WHERE user_id = \\?").
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))

	// Expect SELECT query with ORDER BY and LIMIT
	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE user_id = \\?.*LIMIT").
		WithArgs("user1", 20).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		).
			AddRow("inst_abc123", "user1", "domain1", "db1", "6.5.3", "admin", "admin@example.com", "en_US", false, "", "ready", "", now, now).
			AddRow("inst_xyz789", "user1", "domain2", "db2", nil, "admin2", "admin2@example.com", "en_US", false, "", "installing", "", now, now))

	installs, total, err := repo.ListByUserID(context.Background(), "user1", ListOptions{Limit: 20})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, installs, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallList(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	// Expect COUNT query
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `application_installs`").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(3))

	// Expect SELECT query with ORDER BY and LIMIT
	mock.ExpectQuery("SELECT .* FROM `application_installs`.*LIMIT").
		WithArgs(20).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "domain_id", "db_id", "version", "admin_username", "admin_email", "locale", "use_www", "subdirectory", "status", "last_error", "created_at", "updated_at"},
		).
			AddRow("inst_abc123", "user1", "domain1", "db1", "6.5.3", "admin", "admin@example.com", "en_US", false, "", "ready", "", now, now).
			AddRow("inst_def456", "user2", "domain2", "db2", "6.5.3", "admin", "admin@example.com", "en_US", false, "", "ready", "", now, now).
			AddRow("inst_xyz789", "user1", "domain3", "db3", nil, "admin2", "admin2@example.com", "en_US", false, "", "installing", "", now, now))

	installs, total, err := repo.List(context.Background(), ListOptions{Limit: 20})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, installs, 3)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallUpdateStatus(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `application_installs` SET .* WHERE id = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdateStatus(context.Background(), "inst_abc123", "ready", nil, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallUpdateStatus_WithErrorAndVersion(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	errMsg := "installation failed"
	version := "6.5.3"

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `application_installs` SET .* WHERE id = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdateStatus(context.Background(), "inst_abc123", "failed", &errMsg, &version)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallDelete_Success(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `application_installs` WHERE id = \\?").
		WithArgs("inst_abc123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "inst_abc123")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWordPressInstallDelete_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `application_installs` WHERE id = \\?").
		WithArgs("inst_nonexistent").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "inst_nonexistent")
	require.Error(t, err)
	require.Equal(t, ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #378: the reconciler's WordPress drift probe must only see WordPress
// installs — the query has to scope status='ready' AND app_type='wordpress',
// otherwise non-WordPress apps (itflow, dokuwiki, …) get falsely flagged as
// drifted. This asserts both predicates are in the SQL.
func TestListReadyByUpdatedAtAsc_ScopesToWordPress(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewWordPressInstallRepository(db)
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM `application_installs` WHERE status = \\? AND app_type = \\?.*ORDER BY updated_at ASC.*LIMIT").
		WithArgs("ready", "wordpress", 100).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "status", "app_type", "updated_at"},
		).AddRow("inst_wp", "ready", "wordpress", now))

	rows, err := repo.ListReadyByUpdatedAtAsc(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "inst_wp", rows[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
