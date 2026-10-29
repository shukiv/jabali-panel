package repository_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2328: a delete names the domain as well as the id, so an admin acting
// on one domain's page cannot remove another domain's path by its id.
func TestBotChallengeExclusion_DeleteOtherDomainIsNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewBotChallengeExclusionRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `bot_challenge_exclusions` WHERE id = ? AND domain_id = ?")).
		WithArgs("excl-1", "dom-b").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := repo.DeleteForDomain(context.Background(), "dom-b", "excl-1")
	require.True(t, errors.Is(err, repository.ErrNotFound), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBotChallengeExclusion_CreateDuplicateIsConflict(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewBotChallengeExclusionRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `bot_challenge_exclusions`")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'x' for key 'uq_bot_challenge_excl'"})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), &models.BotChallengeExclusion{ID: "excl-1", DomainID: "dom-a", PathPrefix: "/api/"})
	require.True(t, errors.Is(err, repository.ErrConflict), "got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}
