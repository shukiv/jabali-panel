package backupscheduleops

// GH #2300: an account's "Days to keep" (keep_within_days) keeps every backup
// of the last N days. Whoever sets it on an account's own schedule, it must
// fit in the plan's backup limit at the schedule's frequency, as on the
// account's page. planPkg allows 5 backups.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

func TestCreateForAccount_KeepWithinDays(t *testing.T) {
	d, w, _ := tenantRowDeps(internalbackup.CadenceDaily)
	in := accountInput() // every 6 hours: 5 backups hold 1 day
	in.KeepDaily = nil
	in.KeepWithinDays = intPtr(1)
	_, err := CreateForAccount(context.Background(), d, in)
	require.NoError(t, err)
	require.Len(t, w.calls, 1)
	require.Equal(t, 1, *w.calls[0].s.KeepWithinDays)

	d, w, _ = tenantRowDeps(internalbackup.CadenceDaily)
	in.KeepWithinDays = intPtr(2) // 8 backups
	_, err = CreateForAccount(context.Background(), d, in)
	require.ErrorIs(t, err, ErrKeepDaysOutOfRange)
	require.Empty(t, w.calls)
}

func TestUpdate_AccountSchedule_KeepWithinDays(t *testing.T) {
	// Daily on a plan of 5: up to 5 days.
	d, w, _ := tenantRowDeps(internalbackup.CadenceDaily)
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", KeepWithinDays: intPtr(5)})
	require.NoError(t, err)
	require.Equal(t, 5, *s.KeepWithinDays)
	require.Len(t, w.updates, 1)

	d, w, _ = tenantRowDeps(internalbackup.CadenceDaily)
	_, err = Update(context.Background(), d, UpdateInput{ID: "s1", KeepWithinDays: intPtr(6)})
	require.ErrorIs(t, err, ErrKeepDaysOutOfRange)
	require.Empty(t, w.updates)

	// A new frequency is checked against the days the schedule keeps.
	d, w, _ = tenantRowDeps(internalbackup.CadenceDaily)
	w.getRow.KeepWithinDays = intPtr(5)
	every6h := internalbackup.CadenceEvery6h
	_, err = Update(context.Background(), d, UpdateInput{ID: "s1", Cadence: &every6h})
	require.ErrorIs(t, err, ErrKeepDaysOutOfRange)
	require.Empty(t, w.updates)
}
