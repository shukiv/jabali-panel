package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestCacheWarmupRun_LatestForInstall_NotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCacheWarmupRunRepository(gdb)

	mock.ExpectQuery("cache_warmup_runs.+WHERE install_id = ").
		WithArgs("01INSTALL0000000000000000", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // no rows

	_, err := repo.LatestForInstall(context.Background(), "01INSTALL0000000000000000")
	require.True(t, errors.Is(err, repository.ErrNotFound), "no rows -> ErrNotFound, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCacheWarmupRun_LatestForInstall_ReturnsNewest(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCacheWarmupRunRepository(gdb)

	mock.ExpectQuery("cache_warmup_runs.+ORDER BY created_at DESC").
		WithArgs("01INSTALL0000000000000000", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "install_id", "host", "state", "warmed"}).
			AddRow("01RUN00000000000000000000", "01INSTALL0000000000000000", "ex.com", models.CacheWarmupStateDone, 12))

	got, err := repo.LatestForInstall(context.Background(), "01INSTALL0000000000000000")
	require.NoError(t, err)
	require.Equal(t, "ex.com", got.Host)
	require.Equal(t, models.CacheWarmupStateDone, got.State)
	require.Equal(t, 12, got.Warmed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCacheWarmupRun_Create(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCacheWarmupRunRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `cache_warmup_runs`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(context.Background(), &models.CacheWarmupRun{
		ID: "01RUN00000000000000000000", InstallID: "01INSTALL0000000000000000",
		Host: "ex.com", State: models.CacheWarmupStateRunning,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
