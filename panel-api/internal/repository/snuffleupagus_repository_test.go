package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2262: disabling a rule said "Rule disabled" and left it on. The
// override's Enabled field had a gorm default:1 tag, so GORM sent 1 in
// place of false and the upsert stored the rule as enabled.
func TestSnuffleupagusUpsertOverride_DisableStoresEnabledFalse(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	reason := "false positive"
	by := "01ADMIN0000000000000000000"
	ov := &models.SnuffleupagusRuleOverride{
		RuleName:    "10-functions.rules#12 sp.disable_function:shell_exec",
		Enabled:     false,
		Reason:      &reason,
		SetByUserID: &by,
		SetAt:       time.Now().UTC(),
	}

	// MariaDB: GORM reads set_at back with RETURNING, so this is a query.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO `snuffleupagus_rule_overrides`").
		WithArgs(ov.RuleName, false, reason, by, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"set_at"}).AddRow(ov.SetAt))
	mock.ExpectCommit()

	require.NoError(t, repository.NewSnuffleupagusRepository(db).UpsertOverride(context.Background(), ov))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSnuffleupagusDeleteAllIncidents_ReturnsTheCount(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	mock.ExpectExec("DELETE FROM snuffleupagus_incidents").WillReturnResult(sqlmock.NewResult(0, 7))

	n, err := repository.NewSnuffleupagusRepository(db).DeleteAllIncidents(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 7, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSnuffleupagusLatestIncidentTs(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewSnuffleupagusRepository(db)

	at := time.Date(2026, 10, 13, 17, 30, 20, 695330000, time.UTC)
	mock.ExpectQuery("SELECT MAX\\(ts\\) FROM `snuffleupagus_incidents`").
		WillReturnRows(sqlmock.NewRows([]string{"MAX(ts)"}).AddRow(at))
	got, err := repo.LatestIncidentTs(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Equal(at), "got %v", got)

	mock.ExpectQuery("SELECT MAX\\(ts\\) FROM `snuffleupagus_incidents`").
		WillReturnRows(sqlmock.NewRows([]string{"MAX(ts)"}).AddRow(nil))
	got, err = repo.LatestIncidentTs(context.Background())
	require.NoError(t, err)
	require.Nil(t, got, "an empty table has no latest incident")
	require.NoError(t, mock.ExpectationsWereMet())
}
