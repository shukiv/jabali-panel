package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// Reuses newMockBackupDB from backup_job_repository_test.go (same package).
// Fetch runs one GROUP BY per metric in a fixed order:
// domains, databases, docker_apps, mailboxes, backup_jobs, bw_daily.

func urStatRows(pairs ...any) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"user_id", "n"})
	for i := 0; i+1 < len(pairs); i += 2 {
		rows.AddRow(pairs[i], pairs[i+1])
	}
	return rows
}

func TestUserResourceStats_Fetch_EmptyInputSkipsQueries(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUserResourceStatsRepository(db)

	out, err := repo.Fetch(context.Background(), nil, time.Now())
	require.NoError(t, err)
	require.Empty(t, out)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserResourceStats_Fetch_MergesPerMetric(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUserResourceStatsRepository(db)

	const u1, u2 = "01USER0000000000000000001", "01USER0000000000000000002"

	// Table names are backticked in the SQL (`databases` is a MariaDB reserved
	// word) — \x60 is the backtick in these regex matchers.
	// Ordered mock — one ExpectQuery per Fetch query, in order. The two
	// `databases`/`mailboxes` passes are told apart by COUNT vs SUM(...).
	mock.ExpectQuery("FROM \x60domains\x60 WHERE").WillReturnRows(urStatRows(u1, 3, u2, 1))
	mock.ExpectQuery("COUNT.*FROM \x60databases\x60").WillReturnRows(urStatRows(u1, 2))
	mock.ExpectQuery("FROM \x60docker_apps\x60").WillReturnRows(urStatRows(u2, 4))
	mock.ExpectQuery("COUNT.*FROM \x60mailboxes\x60").WillReturnRows(urStatRows(u1, 5))
	mock.ExpectQuery("FROM \x60backup_jobs\x60").WillReturnRows(urStatRows(u1, 6))
	mock.ExpectQuery("FROM \x60bw_daily\x60").WillReturnRows(urStatRows(u1, 7340032))
	mock.ExpectQuery("SUM.size_bytes.").WillReturnRows(urStatRows(u1, 5000000))
	mock.ExpectQuery("SUM.m.last_usage_bytes.").WillReturnRows(urStatRows(u1, 900000))

	monthStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	out, err := repo.Fetch(context.Background(), []string{u1, u2}, monthStart)
	require.NoError(t, err)

	require.Equal(t, int64(3), out[u1].Domains)
	require.Equal(t, int64(2), out[u1].Databases)
	require.Equal(t, int64(5), out[u1].Mailboxes)
	require.Equal(t, int64(6), out[u1].Backups)
	require.Equal(t, uint64(7340032), out[u1].BandwidthBytes)
	require.Equal(t, uint64(5000000), out[u1].DBBytes)
	require.Equal(t, uint64(900000), out[u1].MailBytes)
	require.Equal(t, int64(0), out[u1].DockerApps)

	require.Equal(t, int64(1), out[u2].Domains)
	require.Equal(t, int64(4), out[u2].DockerApps)
	require.Equal(t, uint64(0), out[u2].BandwidthBytes)

	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #1242: one failing query (e.g. the `databases` reserved-word 1064 that
// zeroed the whole Resources column) must NOT wipe the other metrics — Fetch
// returns the partial map plus an error, and the caller keeps the successes.
func TestUserResourceStats_Fetch_PartialOnQueryError(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUserResourceStatsRepository(db)

	const u1 = "01USER0000000000000000001"
	mock.ExpectQuery("FROM \x60domains\x60 WHERE").WillReturnRows(urStatRows(u1, 3))
	mock.ExpectQuery("COUNT.*FROM \x60databases\x60").WillReturnError(errors.New("1064 reserved word"))
	mock.ExpectQuery("FROM \x60docker_apps\x60").WillReturnRows(urStatRows(u1, 1))
	mock.ExpectQuery("COUNT.*FROM \x60mailboxes\x60").WillReturnRows(urStatRows(u1, 5))
	mock.ExpectQuery("FROM \x60backup_jobs\x60").WillReturnRows(urStatRows(u1, 2))
	mock.ExpectQuery("FROM \x60bw_daily\x60").WillReturnRows(urStatRows(u1, 1024))
	mock.ExpectQuery("SUM.size_bytes.").WillReturnRows(urStatRows(u1, 5000000))
	mock.ExpectQuery("SUM.m.last_usage_bytes.").WillReturnRows(urStatRows(u1, 900000))

	out, err := repo.Fetch(context.Background(), []string{u1}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, err) // the failed metric is surfaced...
	// ...but every other metric survived.
	require.Equal(t, int64(3), out[u1].Domains)
	require.Equal(t, int64(0), out[u1].Databases) // the one that failed
	require.Equal(t, int64(1), out[u1].DockerApps)
	require.Equal(t, int64(5), out[u1].Mailboxes)
	require.Equal(t, int64(2), out[u1].Backups)
	require.Equal(t, uint64(1024), out[u1].BandwidthBytes)
	require.Equal(t, uint64(5000000), out[u1].DBBytes)
	require.Equal(t, uint64(900000), out[u1].MailBytes)
}
