package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1641. `clear` is a hardening-RESTORE action: it must not print "full
// blocking restored" for a host that was never in a mode (a typo would leave the
// real detect row live while the operator believes the host is protected). The
// repository turns a zero-row delete into ErrNotFound so the CLI can refuse.
func TestCRSHostMode_DeleteMissingIsNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCRSHostModeRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `crs_host_modes` WHERE host = ?").
		WithArgs("gone.example.com").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected
	mock.ExpectCommit()

	err := repo.DeleteByHost(context.Background(), "gone.example.com")
	require.True(t, errors.Is(err, repository.ErrNotFound),
		"clear of a host with no mode → ErrNotFound, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCRSHostMode_DeleteExistingOK(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewCRSHostModeRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `crs_host_modes` WHERE host = ?").
		WithArgs("forum.example.com").
		WillReturnResult(sqlmock.NewResult(0, 1)) // 1 row affected
	mock.ExpectCommit()

	require.NoError(t, repo.DeleteByHost(context.Background(), "forum.example.com"))
	require.NoError(t, mock.ExpectationsWereMet())
}
