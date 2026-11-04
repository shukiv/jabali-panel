package repository_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2377: SetPushResult writes only the two push columns. A full-row Save
// here would overwrite an edit saved through Update in between, and bumping
// updated_at would make every push look like a change to the zone.

func TestDNSZoneSetPushResult_SuccessStampsTimeAndClearsError(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDNSZoneRepository(gdb)
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `dns_zones` SET `last_push_error`=?,`last_pushed_at`=? WHERE id = ?")).
		WithArgs(nil, at, "zone-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.SetPushResult(context.Background(), "zone-1", &at, ""))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDNSZoneSetPushResult_FailureKeepsLastPushedAt(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDNSZoneRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `dns_zones` SET `last_push_error`=? WHERE id = ?")).
		WithArgs("Access denied", "zone-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.SetPushResult(context.Background(), "zone-1", nil, "Access denied"))
	require.NoError(t, mock.ExpectationsWereMet())
}
