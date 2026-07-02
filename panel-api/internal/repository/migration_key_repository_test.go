package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #648 S1: the one-time claim guarantee lives in the atomic conditional
// UPDATE (WHERE status='unclaimed' AND expires_at > now). Verify the repo maps
// RowsAffected=0 (already claimed/expired) to ErrNotFound, and that the guard
// clause is actually in the SQL.
func TestMigrationKeyRepo_Claim_OneTime(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMigrationKeyRepository(gdb)
	now := time.Now().UTC()

	// The UPDATE must carry the unclaimed+unexpired guard.
	const guardedUpdate = "UPDATE `migration_keys` SET .*WHERE id = \\? AND status = \\? AND expires_at > \\?"

	// First claim: 1 row affected -> success.
	mock.ExpectBegin()
	mock.ExpectExec(guardedUpdate).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.Claim(context.Background(), "k1", "https://src.example", "sess1", now))

	// Second claim (or expired): 0 rows affected -> ErrNotFound (the guard).
	mock.ExpectBegin()
	mock.ExpectExec(guardedUpdate).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	err := repo.Claim(context.Background(), "k1", "https://src.example", "sess1", now)
	require.ErrorIs(t, err, repository.ErrNotFound)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMigrationKeyRepo_FindByKeyHash(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMigrationKeyRepository(gdb)

	cols := []string{"id", "job_id", "key_hash", "dest_user_id", "dest_domain_id", "dest_docroot", "domain_change", "status", "expires_at", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT \\* FROM `migration_keys` WHERE key_hash = \\?").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(
			"k1", "j1", "abc", "u1", "d1", "/home/u/public_html", 0,
			models.MigrationKeyUnclaimed, time.Now().Add(time.Hour), time.Now(), time.Now()))
	got, err := repo.FindByKeyHash(context.Background(), "abc")
	require.NoError(t, err)
	require.Equal(t, "k1", got.ID)
	require.True(t, got.IsClaimable(time.Now()))

	// Empty hash short-circuits to ErrNotFound (no query).
	_, err = repo.FindByKeyHash(context.Background(), "")
	require.ErrorIs(t, err, repository.ErrNotFound)

	require.NoError(t, mock.ExpectationsWereMet())
}
