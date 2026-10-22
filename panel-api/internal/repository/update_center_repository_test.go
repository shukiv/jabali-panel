package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Saving the Updates settings writes security_enabled too; leaving it out of
// the update list would keep whatever the row had, and an admin's change
// would silently not stick.
func TestUpdateAutoupdateConfig_UpsertWritesSecurityEnabled(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUpdateAutoupdateConfigRepository(db)

	mock.ExpectBegin()
	// The explicit false is sent, not left to the column default.
	mock.ExpectQuery("INSERT INTO `update_autoupdate_config`.*`security_enabled`.*ON DUPLICATE KEY UPDATE.*`security_enabled`=VALUES\\(`security_enabled`\\)").
		WithArgs(1, true, false, "03:30", false, "04:30", false, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	require.NoError(t, repo.Upsert(context.Background(), &models.UpdateAutoupdateConfig{
		AptEnabled: true, AptTime: "03:30", JabaliTime: "04:30", SecurityEnabled: false,
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A fresh install applies security advisories automatically: the seeded row
// has security_enabled on, like apt_enabled.
func TestUpdateAutoupdateConfig_EnsureDefaultSeedsSecurityEnabled(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUpdateAutoupdateConfigRepository(db)

	mock.ExpectQuery("SELECT \\* FROM `update_autoupdate_config`").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO `update_autoupdate_config`.*`security_enabled`").
		WithArgs(1, true, false, "03:30", false, "04:30", true, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	row, err := repo.EnsureDefault(context.Background())
	require.NoError(t, err)
	require.True(t, row.SecurityEnabled)
	require.NoError(t, mock.ExpectationsWereMet())
}
