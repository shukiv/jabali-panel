package backupscheduler

import (
	"context"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// pendingFakeJobs records Create calls and answers HasPendingForTarget from a
// flag so the skip-if-pending guard can be exercised without a DB.
type pendingFakeJobs struct {
	repository.BackupJobRepository
	pending bool
	created int
	// lastTarget captures the (schedule, dest, user) the guard queried.
	qSched, qDest, qUser string
}

func (f *pendingFakeJobs) HasPendingForTarget(_ context.Context, scheduleID, destinationID, userID string) (bool, error) {
	f.qSched, f.qDest, f.qUser = scheduleID, destinationID, userID
	return f.pending, nil
}
func (f *pendingFakeJobs) Create(context.Context, *models.BackupJob) error { f.created++; return nil }

func schedulerWithJobs(j repository.BackupJobRepository) *Scheduler {
	return &Scheduler{deps: Deps{Jobs: j, Log: slog.Default()}}
}

// JAB-361: the exact starvation case — a system_backup schedule against a
// dead destination must NOT enqueue another job while one is still
// queued/running, or the cadence piles jobs up faster than the 4h-per-slot
// drain and starves every other backup.
func TestEnqueueSystemBackup_SkipsWhenPending(t *testing.T) {
	jobs := &pendingFakeJobs{pending: true}
	s := schedulerWithJobs(jobs)
	ok := s.enqueueSystemBackup(context.Background(),
		models.BackupSchedule{ID: "sch1"}, &models.BackupDestination{ID: "dead-dest"}, "run1")
	if ok {
		t.Error("enqueueSystemBackup must return false when a job is already pending")
	}
	if jobs.created != 0 {
		t.Errorf("no job may be created when one is already pending; created=%d", jobs.created)
	}
	if jobs.qSched != "sch1" || jobs.qDest != "dead-dest" || jobs.qUser != "system" {
		t.Errorf("guard queried the wrong target: %q/%q/%q", jobs.qSched, jobs.qDest, jobs.qUser)
	}
}

func TestEnqueueSystemBackup_CreatesWhenNonePending(t *testing.T) {
	jobs := &pendingFakeJobs{pending: false}
	s := schedulerWithJobs(jobs)
	ok := s.enqueueSystemBackup(context.Background(),
		models.BackupSchedule{ID: "sch1"}, &models.BackupDestination{ID: "d1"}, "run1")
	if !ok {
		t.Error("enqueueSystemBackup must enqueue when nothing is pending")
	}
	if jobs.created != 1 {
		t.Errorf("exactly one job must be created; created=%d", jobs.created)
	}
}
