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

// The admin backup-schedule update adapter must likewise delegate to the
// shared lifecycle leaf (JAB-307). This is the path that wrote the row,
// destinations, and users as three separate statements; routing it through the
// leaf commits them in one transaction and drops the inline copy of the
// admin-target rejection / kind-gate the leaf already owns. Reintroducing the
// non-atomic Update→ReplaceDestinations→ReplaceUsers sequence reddens this.
func TestBackupScheduleUpdate_RoutesThroughLifecycle(t *testing.T) {
	src, err := os.ReadFile("backup_schedules.go")
	require.NoError(t, err)
	body := scheduleFuncBody(t, string(src), "func (h *backupScheduleHandler) update(")

	require.Contains(t, body, "backupscheduleops.Update(",
		"update must delegate to the shared lifecycle leaf")
	require.Contains(t, body, `writeScheduleOpError(c, err, "db_update")`,
		"update must keep emitting db_update as its generic 500 code")
	require.NotContains(t, body, "Schedules.Update(",
		"update must not write the row directly (non-atomic)")
	require.NotContains(t, body, "Schedules.ReplaceUsers(",
		"update must not link users directly (non-atomic)")
	require.NotContains(t, body, "Schedules.ReplaceDestinations(",
		"update must not link destinations directly (non-atomic)")
}

// Parameterising writeScheduleOpError with a per-operation dbErrCode must not
// silently flip the create path's generic 500 string; create keeps db_create.
func TestBackupScheduleCreate_KeepsDbCreateCode(t *testing.T) {
	src, err := os.ReadFile("backup_schedules.go")
	require.NoError(t, err)
	body := scheduleFuncBody(t, string(src), "func (h *backupScheduleHandler) create(")

	require.Contains(t, body, `writeScheduleOpError(c, err, "db_create")`,
		"create must keep emitting db_create as its generic 500 code")
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
