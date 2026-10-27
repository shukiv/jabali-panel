package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newComposerRun() *models.ComposerRun {
	now := time.Now().UTC()
	return &models.ComposerRun{
		ID: "01JCXMPSRRVN00000000000AB1", UserID: "01USERXX0000000000000000AB", DomainID: "01DOMAIN0000000000000000AB",
		WorkDir: "domains/shared", Command: "install", State: models.ComposerRunRunning, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// A run is inserted with its owner's row locked, and only when neither its
// domain nor its folder has a run in progress: two requests can't both start
// one, and two domains sharing a project folder (GH #2276) can't run
// Composer in it at once.
func TestComposerRun_CreateUnlessRunning(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewComposerRunRepository(gdb)
	run := newComposerRun()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id` FROM `users` WHERE id = .+ FOR UPDATE").
		WithArgs(run.UserID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(run.UserID))
	mock.ExpectQuery("SELECT `id` FROM `domains` WHERE id = .+ AND user_id = ").
		WithArgs(run.DomainID, run.UserID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(run.DomainID))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `composer_runs` WHERE user_id = .+ AND state = .+ AND \\(domain_id = .+ OR work_dir = .+\\)").
		WithArgs(run.UserID, models.ComposerRunRunning, run.DomainID, "domains/shared").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO `composer_runs`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateUnlessRunning(context.Background(), run))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestComposerRun_CreateUnlessRunning_RefusesASecondRun(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewComposerRunRepository(gdb)
	run := newComposerRun()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id` FROM `users` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(run.UserID))
	mock.ExpectQuery("SELECT `id` FROM `domains`").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(run.DomainID))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `composer_runs`").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err := repo.CreateUnlessRunning(context.Background(), run)
	require.True(t, errors.Is(err, repository.ErrComposerRunActive), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain that is gone, or is no longer the run's user's, takes no run.
func TestComposerRun_CreateUnlessRunning_DomainGone(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewComposerRunRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id` FROM `users`").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("u"))
	mock.ExpectQuery("SELECT `id` FROM `domains`").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	err := repo.CreateUnlessRunning(context.Background(), newComposerRun())
	require.True(t, errors.Is(err, repository.ErrNotFound), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestComposerRun_CreateUnlessRunning_UserGone(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewComposerRunRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id` FROM `users`").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	err := repo.CreateUnlessRunning(context.Background(), newComposerRun())
	require.True(t, errors.Is(err, repository.ErrNotFound), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A list is the domain's runs, newest first, without their logs.
func TestComposerRun_ListByDomain(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewComposerRunRepository(gdb)

	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `composer_runs` WHERE domain_id = ").
		WithArgs("dom").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("SELECT `composer_runs`.`id`,.+FROM `composer_runs` WHERE domain_id = .+ ORDER BY created_at DESC LIMIT .+ OFFSET").
		WithArgs("dom", 2, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "state"}).AddRow("r2", "succeeded").AddRow("r1", "failed"))

	runs, total, err := repo.ListByDomain(context.Background(), "dom", 1, 2)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, runs, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}
