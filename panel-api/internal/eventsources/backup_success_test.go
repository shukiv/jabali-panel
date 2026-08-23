package eventsources

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestBackupSuccess_FiresInfoOnSucceeded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-5 * time.Minute)
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "s1", UserID: "u1", Kind: "account_backup",
			Status: models.BackupJobStatusSucceeded, FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	d := Deps{
		Queue: pub, History: &fakeHistory{}, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupSuccessPass(context.Background(), d)
	require.Equal(t, 1, pub.Count())
	env := pub.Last()
	require.Equal(t, "backup.success", env.EventKind)
	require.Equal(t, "info", env.Severity)
	require.Equal(t, "u1", env.UserID)
	require.Contains(t, env.Title, "succeeded")
	require.Contains(t, env.Body, "s1")
}

// A partial (finished-with-warnings) job must still notify — as a warning — so a
// degraded backup is never silent (would otherwise read as "didn't run").
func TestBackupSuccess_FiresWarningOnPartial(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-5 * time.Minute)
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "p1", UserID: "u2", Kind: "system_backup",
			Status: models.BackupJobStatusPartial, FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	d := Deps{
		Queue: pub, History: &fakeHistory{}, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupSuccessPass(context.Background(), d)
	require.Equal(t, 1, pub.Count())
	env := pub.Last()
	require.Equal(t, "backup.success", env.EventKind)
	require.Equal(t, "warning", env.Severity)
	require.Contains(t, env.Title, "warnings")
}

// A running/queued/cancelled job is not "finished" — no notification.
func TestBackupSuccess_IgnoresNonFinishedStatuses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-5 * time.Minute)
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "r1", UserID: "u1", Kind: "account_backup", Status: models.BackupJobStatusRunning, FinishedAt: &finished},
		{ID: "c1", UserID: "u1", Kind: "account_backup", Status: models.BackupJobStatusCancelled, FinishedAt: &finished},
		{ID: "f1", UserID: "u1", Kind: "account_backup", Status: models.BackupJobStatusFailed, FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	d := Deps{
		Queue: pub, History: &fakeHistory{}, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupSuccessPass(context.Background(), d)
	require.Equal(t, 0, pub.Count())
}

// Dedupe: a job already announced within the cool-off window does not re-fire.
func TestBackupSuccess_SkipsAlreadyFired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-2 * time.Minute)
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "s2", UserID: "u1", Kind: "account_backup",
			Status: models.BackupJobStatusSucceeded, FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	hist := &fakeHistory{}
	hist.recordFired("backup.success", "Backup job s2 (account_backup) succeeded. dedupe=backup_job_id=s2",
		now.Add(-1*time.Hour))
	d := Deps{
		Queue: pub, History: hist, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupSuccessPass(context.Background(), d)
	require.Equal(t, 0, pub.Count())
}
