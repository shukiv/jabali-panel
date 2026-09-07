package repository_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ReKeyDomain (GH #1579) moves every aggregate row from the old domain name to
// the new one and returns the rows moved. The SQL is pinned so the WHERE stays
// scoped to the old name (a broad UPDATE would rewrite other domains' history).
func TestDMARC_ReKeyDomain_MovesRowsToNewName(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDMARCAggregateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .dmarc_aggregate. SET .domain.=.?.*WHERE domain = .?`).
		WithArgs("new.com", "old.com").
		WillReturnResult(sqlmock.NewResult(0, 3)) // three rows re-keyed
	mock.ExpectCommit()

	n, err := repo.ReKeyDomain(context.Background(), "old.com", "new.com")
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain that never received a DMARC report moves nothing: zero rows, no error.
func TestDMARC_ReKeyDomain_NoMatchIsZero(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDMARCAggregateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .dmarc_aggregate. SET .domain.=.?.*WHERE domain = .?`).
		WithArgs("new.com", "nodmarc.com").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	n, err := repo.ReKeyDomain(context.Background(), "nodmarc.com", "new.com")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
	require.NoError(t, mock.ExpectationsWereMet())
}
