package backupscheduler

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2245: an admin can cancel a queued job while the dispatcher is already
// handing it to the agent. MarkStarted then finds the row no longer queued,
// and the dispatcher must stop the backup it just started, or the agent runs
// a backup whose row says cancelled and holds that user's job slot.

type cancelledDuringDispatchJobs struct {
	dispatchFakeJobs
}

func (cancelledDuringDispatchJobs) MarkStarted(context.Context, string) error {
	return repository.ErrNotFound
}

func TestDispatch_JobCancelledDuringDispatch_StopsItOnTheAgent(t *testing.T) {
	un := "alice"
	user := &models.User{ID: "u1", Username: &un, Email: "a@x.tld"}
	dest := &models.BackupDestination{ID: "d1", Enabled: true, Kind: "local"}
	destID := "d1"
	const jobID = "01HXXXXXXXXXXXXXXXXXXXXXXX"

	for _, tc := range []struct {
		kind, cancelRPC string
	}{
		{models.BackupJobKindAccountBackup, "backup.cancel"},
		{models.BackupJobKindSystemBackup, "system.backup_cancel"},
	} {
		ag := &captureAgent{}
		s := compressionDispatchScheduler(ag, user, dest)
		s.deps.Jobs = cancelledDuringDispatchJobs{}
		job := models.BackupJob{ID: jobID, UserID: "u1", DestinationID: &destID, Kind: tc.kind}
		if tc.kind == models.BackupJobKindSystemBackup {
			s.dispatchSystem(context.Background(), job)
		} else {
			s.dispatchAccount(context.Background(), job)
		}
		p := ag.paramsFor(tc.cancelRPC)
		if p == nil || p["job_id"] != jobID {
			t.Errorf("%s: %s not sent for the job cancelled during dispatch (params %v)", tc.kind, tc.cancelRPC, p)
		}
	}
}

func TestDispatch_JobStillQueued_IsNotStopped(t *testing.T) {
	un := "alice"
	user := &models.User{ID: "u1", Username: &un, Email: "a@x.tld"}
	dest := &models.BackupDestination{ID: "d1", Enabled: true, Kind: "local"}
	destID := "d1"
	ag := &captureAgent{}
	s := compressionDispatchScheduler(ag, user, dest)

	s.dispatchAccount(context.Background(), models.BackupJob{
		ID: "01HXXXXXXXXXXXXXXXXXXXXXXX", UserID: "u1", DestinationID: &destID, Kind: models.BackupJobKindAccountBackup,
	})

	if ag.paramsFor("backup.create") == nil {
		t.Fatal("backup.create was not dispatched")
	}
	if ag.paramsFor("backup.cancel") != nil {
		t.Error("a normally started job was cancelled")
	}
}
