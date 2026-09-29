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

// A receiver sends one DMARC report per domain for the same day, so the
// duplicate check must include the domain: without it the first domain's
// report hid every other domain's report for that day.
func TestDMARC_ExistsForReport_KeysOnDomain(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDMARCAggregateRepository(gdb)
	start := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24*time.Hour - time.Second)

	mock.ExpectQuery(`SELECT count\(\*\) FROM .dmarc_aggregate. WHERE domain = \? AND reporter = \? AND window_start = \? AND window_end = \?`).
		WithArgs("b.example", "google.com", start, end, 1).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	exists, err := repo.ExistsForReport(context.Background(), "google.com", "b.example", start, end)
	require.NoError(t, err)
	assert.False(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}
