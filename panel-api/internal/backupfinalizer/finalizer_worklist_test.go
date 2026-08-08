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

// pagingJobs models the real repository's paging semantics:
//   - ListAll pages the newest rows across ALL statuses (created_at DESC)
//   - ListRunning queries status='running' directly (started_at ASC)
//
// The dispatcher admits queued work oldest-first, so in a big fan-out the
// RUNNING jobs are the oldest-created ones — precisely the rows that fall
// off the end of a newest-N page.
type pagingJobs struct {
	repository.BackupJobRepository
	all         []models.BackupJob // newest-first, as ListAll returns
	finishedIDs []string
	statusCalls int
}

func (j *pagingJobs) ListAll(_ context.Context, limit, _ int) ([]models.BackupJob, int64, error) {
	if limit > len(j.all) {
		limit = len(j.all)
	}
	return j.all[:limit], int64(len(j.all)), nil
}

func (j *pagingJobs) ListRunning(_ context.Context, limit int) ([]models.BackupJob, error) {
	var out []models.BackupJob
	for _, job := range j.all {
		if job.Status == models.BackupJobStatusRunning {
			out = append(out, job)
		}
	}
	// started_at ASC — oldest-started first.
	for i, k := 0, len(out)-1; i < k; i, k = i+1, k-1 {
		out[i], out[k] = out[k], out[i]
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (j *pagingJobs) MarkFinished(_ context.Context, id, _ string, _, _ string,
	_, _ uint64, _, _ json.RawMessage, _ string) error {
	j.finishedIDs = append(j.finishedIDs, id)
	return nil
}

type countingAgent struct {
	reply string
	jobs  *pagingJobs
}

func (a *countingAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	a.jobs.statusCalls++
	return json.RawMessage(a.reply), nil
}

// TestTickOnce_FinalizesRunningJobsBeyondTheNewestPage is the queue-wedge
// regression.
//
// Scenario: one schedule fans out; two jobs started running (created first),
// then 30 more jobs were created and sit queued. The finalizer's page size is
// 25. Under the old ListAll(25) + filter-in-Go, the newest 25 rows are all
// queued, so the two running jobs are invisible: they can never be finalized
// AND never stall-timed-out, they hold their dispatcher slots forever, and
// every subsequent scheduled backup silently stops.
func TestTickOnce_FinalizesRunningJobsBeyondTheNewestPage(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute)
	var all []models.BackupJob
	// Newest-first: 30 queued rows created after the running ones.
	for i := 0; i < 30; i++ {
		all = append(all, models.BackupJob{
			ID:     "01HQUEUED" + string(rune('A'+i%26)) + "000000000000000",
			Kind:   models.BackupJobKindAccountBackup,
			Status: models.BackupJobStatusQueued,
		})
	}
	// ...and the two oldest rows, which are the ones actually running.
	for _, id := range []string{"01HRUNNING1000000000000000", "01HRUNNING2000000000000000"} {
		all = append(all, models.BackupJob{
			ID:        id,
			UserID:    "01HUSERUSERUSERUSERUSER000",
			Kind:      models.BackupJobKindAccountBackup,
			Status:    models.BackupJobStatusRunning,
			StartedAt: &started,
		})
	}

	jobs := &pagingJobs{all: all}
	// Manifest present ⇒ each running job should be sealed.
	agent := &countingAgent{
		jobs:  jobs,
		reply: `{"manifest_found":true,"snapshots":[{"id":"snapM","tags":["stage=manifest"]}]}`,
	}
	f := &Finalizer{deps: Deps{
		Jobs:  jobs,
		Agent: agent,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	f.tickOnce(context.Background())

	if len(jobs.finishedIDs) != 2 {
		t.Fatalf("finalized %v, want both running jobs sealed. A running job outside "+
			"the newest-page window is never finalized and never stall-failed, so it "+
			"holds a dispatcher slot forever and wedges the backup queue.",
			jobs.finishedIDs)
	}
	if jobs.statusCalls != 2 {
		t.Errorf("backup.status called %d times, want 2 (one per running job)", jobs.statusCalls)
	}
}

// A job past the stall timeout is still failed rather than polled forever.
func TestTickOnce_StallTimeoutStillApplies(t *testing.T) {
	stale := time.Now().Add(-(StallTimeout + time.Hour))
	jobs := &pagingJobs{all: []models.BackupJob{{
		ID:        "01HSTALLED00000000000000AA",
		Kind:      models.BackupJobKindAccountBackup,
		Status:    models.BackupJobStatusRunning,
		StartedAt: &stale,
	}}}
	agent := &countingAgent{jobs: jobs, reply: `{"manifest_found":false}`}
	f := &Finalizer{deps: Deps{
		Jobs:  jobs,
		Agent: agent,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	f.tickOnce(context.Background())

	if len(jobs.finishedIDs) != 1 {
		t.Fatalf("stalled job should be marked failed, got %v", jobs.finishedIDs)
	}
	if jobs.statusCalls != 0 {
		t.Errorf("a stalled job should not be polled, got %d status calls", jobs.statusCalls)
	}
}

// Restore jobs are sealed synchronously by the API handler; the finalizer
// must skip them even though they appear as running.
func TestTickOnce_SkipsRestoreJobs(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	jobs := &pagingJobs{all: []models.BackupJob{{
		ID:        "01HRESTORE00000000000000AA",
		Kind:      models.BackupJobKindAccountRestore,
		Status:    models.BackupJobStatusRunning,
		StartedAt: &started,
	}}}
	agent := &countingAgent{jobs: jobs, reply: `{"manifest_found":true}`}
	f := &Finalizer{deps: Deps{
		Jobs:  jobs,
		Agent: agent,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	f.tickOnce(context.Background())

	if len(jobs.finishedIDs) != 0 || jobs.statusCalls != 0 {
		t.Errorf("restore job should be skipped; finished=%v statusCalls=%d",
			jobs.finishedIDs, jobs.statusCalls)
	}
}
