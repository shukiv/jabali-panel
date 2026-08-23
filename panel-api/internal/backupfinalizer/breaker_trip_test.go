package backupfinalizer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type breakerJobs struct {
	repository.BackupJobRepository
	running  []models.BackupJob
	finished []string
}

func (f *breakerJobs) ListRunning(context.Context, int) ([]models.BackupJob, error) {
	return f.running, nil
}
func (f *breakerJobs) MarkFinished(_ context.Context, id, _ string, _, _ string, _, _ uint64, _, _ json.RawMessage, _ string) error {
	f.finished = append(f.finished, id)
	return nil
}

type breakerDests struct {
	repository.BackupDestinationRepository
	got              *models.BackupDestination
	recordedID       string
	recordedFailures int
}

func (f *breakerDests) Get(context.Context, string) (*models.BackupDestination, error) {
	return f.got, nil
}
func (f *breakerDests) RecordFailure(_ context.Context, id string, failures int, _ time.Time) error {
	f.recordedID, f.recordedFailures = id, failures
	return nil
}

// JAB-362: a 4h stall-failure of a backup job trips its destination's breaker,
// incrementing the existing consecutive-failure count.
func TestFinalizer_StallTripsDestinationBreaker(t *testing.T) {
	dead := "dead-dest"
	jobs := &breakerJobs{running: []models.BackupJob{{
		ID:            "j1",
		Kind:          models.BackupJobKindAccountBackup,
		Status:        models.BackupJobStatusRunning,
		StartedAt:     startedAgo(5 * time.Hour), // past the 4h StallTimeout
		DestinationID: &dead,
	}}}
	dests := &breakerDests{got: &models.BackupDestination{ID: dead, ConsecutiveFailures: 2}}
	f := &Finalizer{deps: Deps{
		Jobs:         jobs,
		Destinations: dests,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	f.tickOnce(context.Background())

	if len(jobs.finished) != 1 || jobs.finished[0] != "j1" {
		t.Fatalf("stalled job must be marked failed; finished=%v", jobs.finished)
	}
	if dests.recordedID != dead {
		t.Fatalf("breaker tripped on wrong destination: %q", dests.recordedID)
	}
	if dests.recordedFailures != 3 {
		t.Fatalf("breaker must increment existing count (2) to 3; got %d", dests.recordedFailures)
	}
}
