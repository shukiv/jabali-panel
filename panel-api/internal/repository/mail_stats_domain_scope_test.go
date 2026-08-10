package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestDomainSeriesForUser_ScopesByOwner is the security guard for GH #873
// round 4: the tenant traffic query MUST restrict to the caller's own domains
// via a `domain IN (SELECT name FROM domains WHERE user_id = ?)` subquery, with
// the caller id bound as a parameter. It asserts the rendered SQL + args so a
// refactor (or GORM upgrade) that drops the scope fails here instead of leaking
// one tenant's mail volume to another.
func TestDomainSeriesForUser_ScopesByOwner(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewMailStatsRepository(db)

	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM .mail_stats_domain_samples. WHERE sampled_at >= \? AND domain IN \(SELECT .name. FROM .domains. WHERE user_id = \?\)`).
		WithArgs(since, "u_alice").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "metric", "sampled_at", "value"}).
			AddRow("alice.example", "sent", since, int64(3)))

	rows, err := repo.DomainSeriesForUser(context.Background(), since, "u_alice")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "alice.example", rows[0].Domain)
	require.NoError(t, mock.ExpectationsWereMet(), "the scoping subquery must render exactly")
}

// An empty caller id must NOT run a query (and never returns unscoped rows).
func TestDomainSeriesForUser_EmptyUserNoQuery(t *testing.T) {
	db, _, raw := newMockDB(t)
	defer raw.Close()
	repo := NewMailStatsRepository(db)

	rows, err := repo.DomainSeriesForUser(context.Background(), time.Now(), "")
	require.NoError(t, err)
	require.Empty(t, rows)
	// No mock.ExpectQuery registered → any query would fail ExpectationsWereMet.
}
