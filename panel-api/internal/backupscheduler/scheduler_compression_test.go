package backupscheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1646: a Full Server backup fans out one queued account_backup job per
// user, each stamped with the operator's chosen restic compression level. The
// dispatcher must forward that level (backup_jobs.compression) to the agent on
// backup.create — and the same column carries it on a system_backup job to
// system.backup — or the fanned-out account snapshots ignore the choice.

// captureAgent records the params of the last dispatch per command.
type captureAgent struct {
	mu     sync.Mutex
	params map[string]map[string]any
}

func (a *captureAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.params == nil {
		a.params = map[string]map[string]any{}
	}
	if m, ok := params.(map[string]any); ok {
		a.params[command] = m
	}
	return json.RawMessage(`{}`), nil
}

func (a *captureAgent) paramsFor(command string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.params[command]
}

// dispatchFakeJobs no-ops the status transitions the dispatch issues.
type dispatchFakeJobs struct {
	repository.BackupJobRepository
}

func (dispatchFakeJobs) MarkStarted(context.Context, string) error { return nil }
func (dispatchFakeJobs) MarkFinished(context.Context, string, string, string, string, uint64, uint64, json.RawMessage, json.RawMessage, string) error {
	return nil
}

// dispatchFakeUsers resolves the one user dispatchAccount looks up.
type dispatchFakeUsers struct {
	repository.UserRepository
	u *models.User
}

func (f dispatchFakeUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if f.u != nil && f.u.ID == id {
		return f.u, nil
	}
	return nil, repository.ErrNotFound
}

// dispatchFakeDests resolves the destination the job points at.
type dispatchFakeDests struct {
	repository.BackupDestinationRepository
	d *models.BackupDestination
}

func (f dispatchFakeDests) Get(_ context.Context, id string) (*models.BackupDestination, error) {
	if f.d != nil && f.d.ID == id {
		return f.d, nil
	}
	return nil, repository.ErrNotFound
}

func compressionDispatchScheduler(ag *captureAgent, user *models.User, dest *models.BackupDestination) *Scheduler {
	return &Scheduler{deps: Deps{
		Users:        dispatchFakeUsers{u: user},
		Destinations: dispatchFakeDests{d: dest},
		Jobs:         dispatchFakeJobs{},
		Agent:        ag,
		Log:          slog.Default(),
	}}
}

func TestDispatchAccount_ForwardsCompression(t *testing.T) {
	ag := &captureAgent{}
	un := "alice"
	user := &models.User{ID: "u1", Username: &un, Email: "a@x.tld"}
	dest := &models.BackupDestination{ID: "d1", Enabled: true, Kind: "local"}
	s := compressionDispatchScheduler(ag, user, dest)

	destID := "d1"
	s.dispatchAccount(context.Background(), models.BackupJob{
		ID:            "01HXXXXXXXXXXXXXXXXXXXXXXX",
		UserID:        "u1",
		DestinationID: &destID,
		Kind:          models.BackupJobKindAccountBackup,
		Compression:   "max",
	})

	p := ag.paramsFor("backup.create")
	if p == nil {
		t.Fatalf("backup.create was not dispatched")
	}
	if p["compression"] != "max" {
		t.Errorf("compression = %v, want max — the fan-out level must reach the agent", p["compression"])
	}
}

func TestDispatchSystem_ForwardsCompression(t *testing.T) {
	ag := &captureAgent{}
	dest := &models.BackupDestination{ID: "d1", Enabled: true, Kind: "local"}
	s := compressionDispatchScheduler(ag, nil, dest)

	destID := "d1"
	s.dispatchSystem(context.Background(), models.BackupJob{
		ID:            "01HYYYYYYYYYYYYYYYYYYYYYYY",
		UserID:        "system",
		DestinationID: &destID,
		Kind:          models.BackupJobKindSystemBackup,
		Compression:   "max",
	})

	p := ag.paramsFor("system.backup")
	if p == nil {
		t.Fatalf("system.backup was not dispatched")
	}
	if p["compression"] != "max" {
		t.Errorf("compression = %v, want max — the system job level must reach the agent", p["compression"])
	}
}
