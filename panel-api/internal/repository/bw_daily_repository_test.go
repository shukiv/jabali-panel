package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Reuses newMockBackupDB from backup_job_repository_test.go (same
// package — _test.go helpers are visible across files).

func TestBWDaily_Upsert_StampsTimestamps(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `bw_daily`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	row := &models.BWDaily{
		DomainID:      "01J5DOMAIN0000000000000000",
		Day:           time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC),
		BytesTotal:    1234567,
		RequestsTotal: 42,
	}
	require.NoError(t, repo.Upsert(context.Background(), row))
	require.False(t, row.UpdatedAt.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBWDaily_SumForDomain_NoRowsReturnsZeros(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectQuery("FROM `bw_daily`").
		WillReturnRows(sqlmock.NewRows([]string{"bytes", "reqs"}).AddRow(0, 0))

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	bytes, reqs, err := repo.SumForDomain(context.Background(), "01J5DOMAIN0000000000000000", from, to)
	require.NoError(t, err)
	require.Equal(t, uint64(0), bytes)
	require.Equal(t, uint64(0), reqs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBWDaily_SumForDomain_SumsRows(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectQuery("FROM `bw_daily`").
		WillReturnRows(sqlmock.NewRows([]string{"bytes", "reqs"}).AddRow(123456789, 999))

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	bytes, reqs, err := repo.SumForDomain(context.Background(), "01J5DOMAIN0000000000000000", from, time.Time{})
	require.NoError(t, err)
	require.Equal(t, uint64(123456789), bytes)
	require.Equal(t, uint64(999), reqs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBWDaily_SumByDomainIDs_EmptyShortCircuits(t *testing.T) {
	db, _, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	got, err := repo.SumByDomainIDs(context.Background(), nil, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = repo.SumByDomainIDs(context.Background(), []string{}, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestBWDaily_SumByDomainIDs_GroupsByDomain(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectQuery("FROM `bw_daily`").
		WillReturnRows(sqlmock.NewRows([]string{"domain_id", "bytes"}).
			AddRow("01J5DOMAINAAAAAAAAAAAAAAAA", 1000).
			AddRow("01J5DOMAINBBBBBBBBBBBBBBBB", 2000))

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	got, err := repo.SumByDomainIDs(
		context.Background(),
		[]string{"01J5DOMAINAAAAAAAAAAAAAAAA", "01J5DOMAINBBBBBBBBBBBBBBBB"},
		from,
		time.Time{},
	)
	require.NoError(t, err)
	require.Equal(t, uint64(1000), got["01J5DOMAINAAAAAAAAAAAAAAAA"])
	require.Equal(t, uint64(2000), got["01J5DOMAINBBBBBBBBBBBBBBBB"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBWDaily_SumPerDayForDomain_OrdersAsc(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectQuery("FROM `bw_daily`.*ORDER BY day").
		WillReturnRows(sqlmock.NewRows([]string{"day", "bytes_total", "requests_total"}).
			AddRow(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), 100, 1).
			AddRow(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), 200, 2).
			AddRow(time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC), 300, 3))

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	got, err := repo.SumPerDayForDomain(context.Background(), "01J5DOMAIN0000000000000000", from, to)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, uint64(100), got[0].BytesTotal)
	require.Equal(t, uint64(300), got[2].BytesTotal)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A re-scan replaces the day's country rows: delete then insert, in one
// transaction, stamped with the domain and day.
func TestBWDaily_ReplaceCountriesForDay_DeletesThenInserts(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `bw_daily_country` WHERE domain_id = \\? AND day = \\?").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("INSERT INTO `bw_daily_country`").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	day := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	rows := []models.BWDailyCountry{
		{CountryCode: "US", RequestsTotal: 489, BytesTotal: 2658828},
		{CountryCode: "ZZ", RequestsTotal: 5, BytesTotal: 500},
	}
	require.NoError(t, repo.ReplaceCountriesForDay(context.Background(), "01J5DOMAIN0000000000000000", day, rows))
	for _, r := range rows {
		require.Equal(t, "01J5DOMAIN0000000000000000", r.DomainID)
		require.True(t, r.Day.Equal(day))
		require.False(t, r.UpdatedAt.IsZero())
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBWDaily_ReplaceCountriesForDay_EmptyOnlyDeletes(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `bw_daily_country`").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, repo.ReplaceCountriesForDay(context.Background(), "d1", time.Now().UTC(), nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBWDaily_SumCountriesForDomain_GroupsByCountry(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBWDailyRepository(db)

	mock.ExpectQuery("FROM `bw_daily_country` WHERE domain_id = \\? AND day >= \\? AND day <= \\? GROUP BY `country_code` ORDER BY requests_total DESC, country_code ASC").
		WillReturnRows(sqlmock.NewRows([]string{"country_code", "bytes_total", "requests_total"}).
			AddRow("US", 3000, 500).
			AddRow("DE", 70, 9))

	from := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	got, err := repo.SumCountriesForDomain(context.Background(), "d1", from, to)
	require.NoError(t, err)
	require.Equal(t, []CountryPoint{
		{CountryCode: "US", BytesTotal: 3000, RequestsTotal: 500},
		{CountryCode: "DE", BytesTotal: 70, RequestsTotal: 9},
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
