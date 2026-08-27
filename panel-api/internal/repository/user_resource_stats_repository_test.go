package repository

import (
	"context"
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

	mock.ExpectQuery("FROM domains").WillReturnRows(urStatRows(u1, 3, u2, 1))
	mock.ExpectQuery("FROM databases").WillReturnRows(urStatRows(u1, 2))
	mock.ExpectQuery("FROM docker_apps").WillReturnRows(urStatRows(u2, 4))
	mock.ExpectQuery("FROM mailboxes").WillReturnRows(urStatRows(u1, 5))
	mock.ExpectQuery("FROM backup_jobs").WillReturnRows(urStatRows(u1, 6))
	mock.ExpectQuery("FROM bw_daily").WillReturnRows(urStatRows(u1, 7340032))

	monthStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	out, err := repo.Fetch(context.Background(), []string{u1, u2}, monthStart)
	require.NoError(t, err)

	require.Equal(t, int64(3), out[u1].Domains)
	require.Equal(t, int64(2), out[u1].Databases)
	require.Equal(t, int64(5), out[u1].Mailboxes)
	require.Equal(t, int64(6), out[u1].Backups)
	require.Equal(t, uint64(7340032), out[u1].BandwidthBytes)
	require.Equal(t, int64(0), out[u1].DockerApps)

	require.Equal(t, int64(1), out[u2].Domains)
	require.Equal(t, int64(4), out[u2].DockerApps)
	require.Equal(t, uint64(0), out[u2].BandwidthBytes)

	require.NoError(t, mock.ExpectationsWereMet())
}
