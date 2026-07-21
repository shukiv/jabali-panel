package eventsources

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fake backup-job repo ---

type fakeBackupJobs struct {
	rows []models.BackupJob
}

func (f *fakeBackupJobs) Create(context.Context, *models.BackupJob) error { return nil }
func (f *fakeBackupJobs) Delete(context.Context, string) error            { return nil }
func (f *fakeBackupJobs) Get(context.Context, string) (*models.BackupJob, error) {
	return nil, nil
}
func (f *fakeBackupJobs) OldestAccountBackupForUser(context.Context, string) (*models.BackupJob, error) {
	return nil, nil
}

func (f *fakeBackupJobs) ListForUser(context.Context, string, int, int) ([]models.BackupJob, int64, error) {
	return nil, 0, nil
}
func (f *fakeBackupJobs) ListAll(context.Context, int, int) ([]models.BackupJob, int64, error) {
	return nil, 0, nil
}
func (f *fakeBackupJobs) MarkStarted(context.Context, string) error { return nil }
func (f *fakeBackupJobs) MarkFinished(context.Context, string, string, string, string, uint64, uint64, json.RawMessage, json.RawMessage, string) error {
	return nil
}
func (f *fakeBackupJobs) CountByStatus(context.Context, string) (int64, error) { return 0, nil }
func (f *fakeBackupJobs) ListQueuedOldest(context.Context, int) ([]models.BackupJob, error) {
	return nil, nil
}
func (f *fakeBackupJobs) ListRuns(context.Context, int, int) ([]repository.BackupRunSummary, int64, error) {
	return nil, 0, nil
}
func (f *fakeBackupJobs) ListByRun(context.Context, string) ([]models.BackupJob, error) {
	return nil, nil
}
func (f *fakeBackupJobs) ListManual(context.Context, int, int) ([]models.BackupJob, int64, error) {
	return nil, 0, nil
}
func (f *fakeBackupJobs) ListByStatusSince(_ context.Context, status string, since time.Time, _ int) ([]models.BackupJob, error) {
	var out []models.BackupJob
	for _, r := range f.rows {
		if r.Status != status {
			continue
		}
		if r.FinishedAt == nil || r.FinishedAt.Before(since) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func TestBackupFail_FiresOnFailedRow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-5 * time.Minute)
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "j1", UserID: "u1", Kind: "account_backup",
			Status: models.BackupJobStatusFailed, ErrorText: "restic exit 1",
			FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	hist := &fakeHistory{}
	d := Deps{
		Queue: pub, History: hist, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupFailPass(context.Background(), d)
	require.Equal(t, 1, pub.Count())
	env := pub.Last()
	require.Equal(t, "backup.fail", env.EventKind)
	require.Equal(t, "critical", env.Severity)
	require.Equal(t, "u1", env.UserID)
	require.Contains(t, env.Body, "j1")
	require.Contains(t, env.Body, "restic exit 1")
}

func TestBackupFail_SkipsAlreadyFiredRow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-2 * time.Minute)
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "j2", UserID: "u1", Kind: "system_backup",
			Status: models.BackupJobStatusFailed, FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	hist := &fakeHistory{}
	// Record a prior fire within the cool-off window so dedupe trips.
	hist.recordFired("backup.fail", "Backup job j2 (system_backup) failed. dedupe=backup_job_id=j2",
		now.Add(-1*time.Hour))
	d := Deps{
		Queue: pub, History: hist, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupFailPass(context.Background(), d)
	require.Equal(t, 0, pub.Count())
}

func TestBackupFail_SkipsRowsOutsideLookback(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-2 * time.Hour) // older than 30m lookback
	jobs := &fakeBackupJobs{rows: []models.BackupJob{
		{ID: "j3", UserID: "u1", Kind: "account_backup",
			Status: models.BackupJobStatusFailed, FinishedAt: &finished},
	}}
	pub := &capturingPublisher{}
	hist := &fakeHistory{}
	d := Deps{
		Queue: pub, History: hist, BackupJobs: jobs,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
	backupFailPass(context.Background(), d)
	require.Equal(t, 0, pub.Count())
}
