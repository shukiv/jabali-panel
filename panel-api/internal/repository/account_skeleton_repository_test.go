package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestAccountSkeleton_DeleteMissingIsNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewAccountSkeletonRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `account_skeleton_files` WHERE rel_path = ?").
		WithArgs("gone.html").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "gone.html")
	require.True(t, errors.Is(err, repository.ErrNotFound), "delete of a missing path → ErrNotFound, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountSkeleton_DeleteExistingOK(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewAccountSkeletonRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `account_skeleton_files` WHERE rel_path = ?").
		WithArgs("index.html").
		WillReturnResult(sqlmock.NewResult(0, 1)) // 1 row affected
	mock.ExpectCommit()

	require.NoError(t, repo.Delete(context.Background(), "index.html"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountSkeleton_Count(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewAccountSkeletonRepository(gdb)

	mock.ExpectQuery("SELECT count.* FROM `account_skeleton_files`").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	n, err := repo.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(3), n)
	require.NoError(t, mock.ExpectationsWereMet())
}
