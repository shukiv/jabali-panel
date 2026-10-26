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

func newGitDeployment() *models.DomainGitDeployment {
	now := time.Now().UTC()
	return &models.DomainGitDeployment{
		ID: "01JCXMPSRRVN00000000000AB1", UserID: "01USERXX0000000000000000AB", DomainID: "01DOMAIN0000000000000000AB",
		OSUser: "alice", Kind: models.GitDeployKindDeploy, ReleaseID: "01JCXMPSRRVN00000000000AB1",
		DocRoot: "/home/alice/domains/example.com/current/public", State: models.GitDeployRunning,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// A run is inserted with the domain's row locked, and only when the domain
// has no run in progress, so two requests can't both start one.
func TestGitDeployment_CreateUnlessRunning(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitDeploymentRepository(gdb)
	run := newGitDeployment()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id` FROM `domains` WHERE id = .+ FOR UPDATE").
		WithArgs(run.DomainID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(run.DomainID))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `domain_git_deployments` WHERE domain_id = .+ AND state = ").
		WithArgs(run.DomainID, models.GitDeployRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO `domain_git_deployments`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateUnlessRunning(context.Background(), run))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGitDeployment_CreateUnlessRunning_RefusesASecondRun(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitDeploymentRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id` FROM `domains` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("01DOMAIN0000000000000000AB"))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `domain_git_deployments`").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err := repo.CreateUnlessRunning(context.Background(), newGitDeployment())
	require.True(t, errors.Is(err, repository.ErrGitDeploymentActive), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain's runs are listed for its current owner only: a new owner doesn't
// see the previous owner's deployments and their logs.
func TestGitDeployment_ListByDomainIsTheOwners(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitDeploymentRepository(gdb)

	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `domain_git_deployments` WHERE domain_id = .+ AND user_id = ").
		WithArgs("dom", "owner").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT `domain_git_deployments`.`id`,.+FROM `domain_git_deployments` WHERE domain_id = .+ AND user_id = .+ ORDER BY created_at DESC LIMIT").
		WithArgs("dom", "owner", 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "state"}).AddRow("r1", "succeeded"))

	runs, total, err := repo.ListByDomain(context.Background(), "dom", "owner", 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, runs, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

// The document root is written in one statement that also checks the owner,
// and nothing else of the domain is written.
func TestGitDeployment_SetDocRootChecksTheOwner(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitDeploymentRepository(gdb)
	root := "/home/alice/domains/example.com/current/public"

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET `doc_root`=\\?,`updated_at`=\\? WHERE id = \\? AND user_id = \\? AND doc_root <> \\?$").
		WithArgs(root, sqlmock.AnyArg(), "dom", "owner", root).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	changed, err := repo.SetDocRoot(context.Background(), "dom", "owner", root)
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}
