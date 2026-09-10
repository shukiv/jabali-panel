package api

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The admin backup-schedule create adapter must delegate to the shared
// backupscheduleops lifecycle, not hand-roll the row + membership writes
// (JAB-307). Reintroducing the non-atomic Create→ReplaceDestinations→
// ReplaceUsers sequence in the create path reddens this.
func TestBackupScheduleCreate_RoutesThroughLifecycle(t *testing.T) {
	src, err := os.ReadFile("backup_schedules.go")
	require.NoError(t, err)
	body := scheduleFuncBody(t, string(src), "func (h *backupScheduleHandler) create(")

	require.Contains(t, body, "backupscheduleops.Create(",
		"create must delegate to the shared lifecycle leaf")
	require.NotContains(t, body, "Schedules.Create(",
		"create must not write the row directly (non-atomic)")
	require.NotContains(t, body, "Schedules.ReplaceUsers(",
		"create must not link users directly (non-atomic)")
	require.NotContains(t, body, "Schedules.ReplaceDestinations(",
		"create must not link destinations directly (non-atomic)")
}

// scheduleFuncBody returns the source of the function starting at sig, up to
// the next top-level func declaration.
func scheduleFuncBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	require.Greater(t, i, 0, "signature not found: %s", sig)
	rest := src[i+len(sig):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		return rest[:j]
	}
	return rest
}
