package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ReKeyDomain (GH #1579) moves every TLS-RPT aggregate row from the old domain
// name to the new one and returns the rows moved. The SQL is pinned so the WHERE
// stays scoped to the old name.
func TestTLSRPT_ReKeyDomain_MovesRowsToNewName(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewTLSRPTAggregateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .tlsrpt_aggregate. SET .domain.=.?.*WHERE domain = .?`).
		WithArgs("new.com", "old.com").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	n, err := repo.ReKeyDomain(context.Background(), "old.com", "new.com")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain that never received a TLS-RPT report moves nothing: zero rows, no error.
func TestTLSRPT_ReKeyDomain_NoMatchIsZero(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewTLSRPTAggregateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .tlsrpt_aggregate. SET .domain.=.?.*WHERE domain = .?`).
		WithArgs("new.com", "notlsrpt.com").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	n, err := repo.ReKeyDomain(context.Background(), "notlsrpt.com", "new.com")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// One TLS report can carry a policy block per domain; each block is its own
// duplicate check.
func TestTLSRPT_ExistsForReport_KeysOnDomain(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewTLSRPTAggregateRepository(gdb)
	start := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24*time.Hour - time.Second)

	mock.ExpectQuery(`SELECT count\(\*\) FROM .tlsrpt_aggregate. WHERE domain = \? AND reporter = \? AND window_start = \? AND window_end = \?`).
		WithArgs("b.example", "Reporter TLS", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	exists, err := repo.ExistsForReport(context.Background(), "Reporter TLS", "b.example", start, end)
	require.NoError(t, err)
	assert.True(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}
