package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1641 follow-up: `appsec exclusion rm <id>` on an unknown id used to print
// "removed" and fire an appsec.exclusion_rm audit row while nothing was deleted
// (GORM Delete reports nil for a zero-row delete). DeleteByID now returns
// ErrNotFound so the CLI refuses — same guard as host-mode clear.
func TestCRSRuleExclusion_DeleteMissingIsNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCRSRuleExclusionRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `crs_rule_exclusions` WHERE id = ?").
		WithArgs("gone-id").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected
	mock.ExpectCommit()

	err := repo.DeleteByID(context.Background(), "gone-id")
	require.True(t, errors.Is(err, repository.ErrNotFound),
		"rm of a missing exclusion id → ErrNotFound, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCRSRuleExclusion_DeleteExistingOK(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCRSRuleExclusionRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `crs_rule_exclusions` WHERE id = ?").
		WithArgs("excl-1").
		WillReturnResult(sqlmock.NewResult(0, 1)) // 1 row affected
	mock.ExpectCommit()

	require.NoError(t, repo.DeleteByID(context.Background(), "excl-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}
