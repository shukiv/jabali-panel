package backupscheduler

// JAB-362: the dispatcher caps the slots any one destination may hold and skips
// backed-off destinations, WITHOUT throttling a single-destination server
// (work-conserving two-pass admission).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// slotFakeJobs answers the three queries tickDispatch makes; every other method
// panics (unused here) via the embedded nil interface.
type slotFakeJobs struct {
	repository.BackupJobRepository
	running       int64
	runningByDest map[string]int
	queued        []models.BackupJob
}

func (f *slotFakeJobs) CountByStatus(context.Context, string) (int64, error) { return f.running, nil }
func (f *slotFakeJobs) CountRunningByDestination(context.Context) (map[string]int, error) {
	return f.runningByDest, nil
}
func (f *slotFakeJobs) ListQueuedOldest(_ context.Context, _ int) ([]models.BackupJob, error) {
	return f.queued, nil
}

// slotFakeDests overrides only ListBackedOffIDs.
type slotFakeDests struct {
	repository.BackupDestinationRepository
	backedOff map[string]bool
}

func (f *slotFakeDests) ListBackedOffIDs(_ context.Context, _ time.Time) (map[string]bool, error) {
	return f.backedOff, nil
}

func strptr(s string) *string { return &s }

func qjob(id, dest string) models.BackupJob {
	j := models.BackupJob{ID: id, Kind: models.BackupJobKindAccountBackup}
	if dest != "" {
		j.DestinationID = strptr(dest)
	}
	return j
}

// runDispatch runs one tickDispatch with the given fakes (max defaults to 2 via
// nil Settings) and returns the destination keys dispatched, in order.
func runDispatch(jobs *slotFakeJobs, backedOff map[string]bool) []string {
	var got []string
	s := &Scheduler{
		deps: Deps{
			Jobs:         jobs,
			Destinations: &slotFakeDests{backedOff: backedOff},
			Log:          slog.Default(),
		},
		dispatch: func(_ context.Context, j models.BackupJob) {
			got = append(got, destKey(j))
		},
	}
	s.tickDispatch(context.Background())
	return got
}

func count(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

func TestDispatch_SingleDestinationNotThrottled(t *testing.T) {
	// One destination, two queued, no running → BOTH dispatch (work-conserving).
	jobs := &slotFakeJobs{
		runningByDest: map[string]int{},
		queued:        []models.BackupJob{qjob("j1", "d1"), qjob("j2", "d1")},
	}
	got := runDispatch(jobs, nil)
	if len(got) != 2 || count(got, "d1") != 2 {
		t.Fatalf("single-dest must dispatch both queued jobs; got %v", got)
	}
}

func TestDispatch_PerDestCapPreventsMonopoly(t *testing.T) {
	// d1 floods the front of the queue; d2 must still get a slot (max=2).
	jobs := &slotFakeJobs{
		runningByDest: map[string]int{},
		queued: []models.BackupJob{
			qjob("j1", "d1"), qjob("j2", "d1"), qjob("j3", "d1"), qjob("j4", "d2"),
		},
	}
	got := runDispatch(jobs, nil)
	if count(got, "d1") != 1 || count(got, "d2") != 1 {
		t.Fatalf("cap must give d2 a slot despite d1 flooding; got %v", got)
	}
}

func TestDispatch_ExistingRunningCountsTowardCap(t *testing.T) {
	// d1 already holds its capped slot (running=1); the one free slot goes to d2.
	jobs := &slotFakeJobs{
		running:       1,
		runningByDest: map[string]int{"d1": 1},
		queued:        []models.BackupJob{qjob("j1", "d1"), qjob("j2", "d2")},
	}
	got := runDispatch(jobs, nil)
	if len(got) != 1 || got[0] != "d2" {
		t.Fatalf("free slot must go to d2 (d1 at cap); got %v", got)
	}
}

func TestDispatch_BackedOffDestinationSkipped(t *testing.T) {
	jobs := &slotFakeJobs{
		runningByDest: map[string]int{},
		queued:        []models.BackupJob{qjob("j1", "d1"), qjob("j2", "d1"), qjob("j3", "d2")},
	}
	got := runDispatch(jobs, map[string]bool{"d1": true})
	if count(got, "d1") != 0 {
		t.Fatalf("backed-off d1 must not be dispatched; got %v", got)
	}
	if count(got, "d2") != 1 {
		t.Fatalf("healthy d2 must dispatch while d1 is backed off; got %v", got)
	}
}
