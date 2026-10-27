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

// GH #2276: saving a domain's project folder again replaces it.
func TestDomainComposerSettings_PutReplaces(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainComposerSettingsRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `domain_composer_settings` .+ ON DUPLICATE KEY UPDATE `project_dir`=VALUES\\(`project_dir`\\),`updated_at`=VALUES\\(`updated_at`\\)").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.Put(context.Background(), &models.DomainComposerSettings{DomainID: "01DOMAIN0000000000000000AB", ProjectDir: "domains"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain with no project folder is ErrNotFound: it finds composer.json on
// its own.
func TestDomainComposerSettings_GetNone(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainComposerSettingsRepository(gdb)

	mock.ExpectQuery("SELECT \\* FROM `domain_composer_settings` WHERE domain_id = ").
		WithArgs("01DOMAIN0000000000000000AB", 1).
		WillReturnRows(sqlmock.NewRows([]string{"domain_id", "project_dir"}))
	_, err := repo.Get(context.Background(), "01DOMAIN0000000000000000AB")
	require.True(t, errors.Is(err, repository.ErrNotFound), "err = %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Clearing a folder that was never set is not an error.
func TestDomainComposerSettings_DeleteNone(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainComposerSettingsRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `domain_composer_settings` WHERE domain_id = ").
		WithArgs("01DOMAIN0000000000000000AB").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	require.NoError(t, repo.Delete(context.Background(), "01DOMAIN0000000000000000AB"))
	require.NoError(t, mock.ExpectationsWereMet())
}
