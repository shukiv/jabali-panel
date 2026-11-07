package repository

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// nearCutoff matches a time about ActiveQueuedWindow before now.
type nearCutoff struct{}

func (nearCutoff) Match(v driver.Value) bool {
	t, ok := v.(time.Time)
	if !ok {
		return false
	}
	d := time.Since(t) - ActiveQueuedWindow
	return d > -time.Minute && d < time.Minute
}

// A running job always counts as pending; a queued one only while it is
// younger than ActiveQueuedWindow. Nothing ends a queued job, so a stuck one
// must not refuse every later start.
func TestHasActiveOfKind_RunningOrRecentlyQueued(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewBackupJobRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `backup_jobs` WHERE user_id = ? AND kind = ? AND (status = ? OR (status = ? AND created_at > ?)) LIMIT ?")).
		WithArgs("u1", models.BackupJobKindAccountRestore, models.BackupJobStatusRunning, models.BackupJobStatusQueued, nearCutoff{}, 1).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	active, err := repo.HasActiveOfKind(context.Background(), "u1", models.BackupJobKindAccountRestore)
	require.NoError(t, err)
	require.True(t, active)
	require.NoError(t, mock.ExpectationsWereMet())
}
