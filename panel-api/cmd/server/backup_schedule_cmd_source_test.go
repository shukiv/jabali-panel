package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The operator CLI backup-schedule create must delegate to the shared
// backupscheduleops lifecycle (JAB-307) so it enforces the same admin-target
// rejection and commits the row + memberships atomically. A regression back to
// the raw repo.Create→ReplaceDestinations→ReplaceUsers sequence reddens this.
func TestBackupScheduleCLICreate_RoutesThroughLifecycle(t *testing.T) {
	src, err := os.ReadFile("backup_schedule_cmd.go")
	require.NoError(t, err)
	s := string(src)
	start := strings.Index(s, "func newBackupScheduleCreateCmd(")
	require.Greater(t, start, 0)
	rest := s[start:]
	end := strings.Index(rest, "\nfunc newBackupScheduleUpdateCmd(")
	require.Greater(t, end, 0, "next command func not found")
	body := rest[:end]

	require.Contains(t, body, "backupscheduleops.Create(",
		"CLI create must delegate to the shared lifecycle leaf")
	require.NotContains(t, body, "repo.Create(",
		"CLI create must not write the row directly (non-atomic)")
	require.NotContains(t, body, "repo.ReplaceUsers(",
		"CLI create must not link users directly (non-atomic)")
	require.NotContains(t, body, "repo.ReplaceDestinations(",
		"CLI create must not link destinations directly (non-atomic)")
}

// The operator CLI backup-schedule update must likewise delegate to the shared
// lifecycle leaf (JAB-307) — this is the path that previously skipped the
// admin-target rejection entirely and wrote the row + memberships in three
// separate statements. A regression back to that reddens this.
func TestBackupScheduleCLIUpdate_RoutesThroughLifecycle(t *testing.T) {
	src, err := os.ReadFile("backup_schedule_cmd.go")
	require.NoError(t, err)
	s := string(src)
	start := strings.Index(s, "func newBackupScheduleUpdateCmd(")
	require.Greater(t, start, 0)
	rest := s[start:]
	end := strings.Index(rest, "\nfunc newBackupScheduleDeleteCmd(")
	require.Greater(t, end, 0, "next command func not found")
	body := rest[:end]

	require.Contains(t, body, "backupscheduleops.Update(",
		"CLI update must delegate to the shared lifecycle leaf")
	require.NotContains(t, body, "repo.Update(",
		"CLI update must not write the row directly (skips admin-target rejection, non-atomic)")
	require.NotContains(t, body, "repo.ReplaceUsers(",
		"CLI update must not link users directly")
	require.NotContains(t, body, "repo.ReplaceDestinations(",
		"CLI update must not link destinations directly")
}
