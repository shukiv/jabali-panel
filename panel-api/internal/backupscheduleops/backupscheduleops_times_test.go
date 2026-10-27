package backupscheduleops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #2300: an interval schedule an admin turns back on waits for its next
// time, as on the account's page, instead of running within a minute for a
// time that passed while it was off. Falsify: drop the re-enable branch → the
// next run stays five hours in the past.
func TestUpdate_IntervalSchedule_ReEnableWaitsForItsNextTime(t *testing.T) {
	d, w, _ := tenantRowDeps(internalbackup.CadenceEvery6h)
	past := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Minute)
	w.getRow.Enabled = false
	w.getRow.NextRunAt = &past
	on := true
	dests := []string{"d1"}
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Enabled: &on, DestinationIDs: &dests})
	require.NoError(t, err)
	require.True(t, s.Enabled)
	require.NotNil(t, s.NextRunAt)
	require.Equal(t, past.Add(6*time.Hour), *s.NextRunAt, "the next of its times after now")
}
