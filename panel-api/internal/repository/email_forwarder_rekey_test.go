package repository_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ReKeyAliasTargets (GH #1579) recomputes each alias forwarder's target as
// <local_part>@<newDomain>. The SQL is pinned so it stays scoped to ALIAS rows
// with a non-null local_part (external forwarders keep their outside target).
func TestForwarder_ReKeyAliasTargets_RewritesAliasesOnly(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewEmailForwarderRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .email_forwarders. SET .target.=CONCAT\(local_part.*domain_id = .*type = .*local_part IS NOT NULL`).
		WithArgs("new.com", sqlmock.AnyArg(), "dom-1", "alias").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	n, err := repo.ReKeyAliasTargets(context.Background(), "dom-1", "new.com")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain with no alias forwarder rewrites nothing: zero rows, no error.
func TestForwarder_ReKeyAliasTargets_NoAliasesIsZero(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewEmailForwarderRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .email_forwarders. SET .target.=CONCAT\(local_part.*domain_id = .*type = .*local_part IS NOT NULL`).
		WithArgs("new.com", sqlmock.AnyArg(), "dom-none", "alias").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	n, err := repo.ReKeyAliasTargets(context.Background(), "dom-none", "new.com")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
	require.NoError(t, mock.ExpectationsWereMet())
}
