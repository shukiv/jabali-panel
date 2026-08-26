package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestUserRepository_Create(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	u := &models.User{
		ID:           "01HRCWR7CKMCBEDF2PYQ7G0D2J",
		Email:        "alice@example.com",
		PasswordHash: "$2a$12$abcdefghijklmnopqrstu",
		IsAdmin:      false,
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO .users.`).
		WithArgs(
			u.ID,
			u.Email,
			nil, // username
			nil, // cli_php_version (GH #256)
			"",  // name_first default
			"",  // name_last default
			u.PasswordHash,
			false,            // is_admin
			nil,              // package_id
			nil,              // linux_uid
			nil,              // mysqladmin_username (SSO shadow, ADR-0022)
			sqlmock.AnyArg(), // mysqladmin_password_enc — GORM emits []byte{} for nil slice
			nil,              // mysqladmin_provisioned_at
			nil,              // pgadmin_username (M37 PG SSO shadow)
			sqlmock.AnyArg(), // pgadmin_password_enc — GORM emits []byte{} for nil slice
			nil,              // pgadmin_provisioned_at
			nil,              // kratos_identity_id (M20)
			false,            // suspended (migration 000132)
			nil,              // suspended_at
			"",               // suspend_reason
			true,             // webmail_enabled (GORM default:1 promotes the zero-value bool)
			false,            // ssh_forwarding_enabled (GH #1229; default OFF)
			uint64(0),        // disk_used_kb (migration 000257; sweeper fills it in)
			uint64(0),        // disk_limit_kb
			nil,              // disk_checked_at — NULL until the first sweep, which is
			// what makes the UI fall back to the per-row fetch
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), u)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_FindByEmail_Found(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM .users. WHERE email = \? ORDER BY .users.\..id. LIMIT \?`).
		WithArgs("alice@example.com", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "email", "name_first", "name_last", "password_hash",
			"is_admin", "linux_uid", "created_at", "updated_at",
		}).AddRow(
			"01HRCWR7CKMCBEDF2PYQ7G0D2J", "alice@example.com", "", "",
			"$2a$12$hash", false, nil, now, now,
		))

	got, err := repo.FindByEmail(context.Background(), "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", got.Email)
	assert.Equal(t, "01HRCWR7CKMCBEDF2PYQ7G0D2J", got.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_FindByEmail_NotFound(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	mock.ExpectQuery(`SELECT \* FROM .users. WHERE email = \?`).
		WithArgs("nobody@example.com", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // no rows

	_, err := repo.FindByEmail(context.Background(), "nobody@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, repository.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_SetSuspended_True(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .users. SET`).
		WithArgs(
			"non-payment",    // suspend_reason
			sqlmock.AnyArg(), // suspended (true)
			sqlmock.AnyArg(), // suspended_at (ptr to now)
			sqlmock.AnyArg(), // updated_at
			"01HRCWR7CKMCBEDF2PYQ7G0D2J",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.SetSuspended(context.Background(), "01HRCWR7CKMCBEDF2PYQ7G0D2J", true, "non-payment")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_SetSuspended_False_ClearsFields(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .users. SET`).
		WithArgs(
			sqlmock.AnyArg(), // suspend_reason ('')
			sqlmock.AnyArg(), // suspended (false)
			sqlmock.AnyArg(), // updated_at — suspended_at = NULL renders inline
			"01HRCWR7CKMCBEDF2PYQ7G0D2J",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.SetSuspended(context.Background(), "01HRCWR7CKMCBEDF2PYQ7G0D2J", false, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_SetSuspended_NotFound(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewUserRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .users. SET`).
		WillReturnResult(sqlmock.NewResult(0, 0)) // zero rows affected → ErrNotFound
	mock.ExpectCommit()

	err := repo.SetSuspended(context.Background(), "missing-id-zzzzzzzzzzzzzzzz", true, "reason")
	require.Error(t, err)
	assert.ErrorIs(t, err, repository.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
