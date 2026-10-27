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

// Saving again replaces every field, the token and the deployment settings
// included: clearing the token clears the column instead of keeping the old
// one.
func TestDomainGitRepo_PutReplacesTheToken(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitRepoRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `domain_git_repos` .+ ON DUPLICATE KEY UPDATE .*`token_enc`=VALUES\\(`token_enc`\\).*" +
		"`web_dir`=VALUES\\(`web_dir`\\),`shared_paths`=VALUES\\(`shared_paths`\\),`deploy_commands`=VALUES\\(`deploy_commands`\\)").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.Put(context.Background(), &models.DomainGitRepo{
		DomainID: "01DOMAIN0000000000000000AB", UserID: "01USERXX0000000000000000AB",
		RepoURL: "https://github.com/acme/shop.git", Branch: "main", VerifiedAt: time.Now().UTC(),
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainGitRepo_DeleteMissing(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitRepoRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `domain_git_repos` WHERE domain_id = ").
		WithArgs("01DOMAIN0000000000000000AB").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	err := repo.Delete(context.Background(), "01DOMAIN0000000000000000AB")
	require.True(t, errors.Is(err, repository.ErrNotFound), "err = %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #2277: a domain that changes owner leaves its Git settings, and the old
// owner's token, behind, in the same transaction as the owner change.
func TestDomain_TransferOwnerDropsTheGitSettings(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT \\* FROM `python_apps` WHERE domain_id = ").
		WithArgs("01DOMAIN0000000000000000AB").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("UPDATE `domains` SET .+ WHERE id = ").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM `domain_git_repos` WHERE domain_id = ").
		WithArgs("01DOMAIN0000000000000000AB").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// GH #2276: and its Composer project folder, a path in the old owner's
	// home.
	mock.ExpectExec("DELETE FROM `domain_composer_settings` WHERE domain_id = ").
		WithArgs("01DOMAIN0000000000000000AB").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.TransferOwner(context.Background(), "01DOMAIN0000000000000000AB", "01NEWUSR0000000000000000AB", "/home/bob/domains/example.com/public_html"))
	require.NoError(t, mock.ExpectationsWereMet())
}
