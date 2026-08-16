package backupscheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// standbyFakeSettings satisfies repository.ServerSettingsRepository via
// embedding; only Get is implemented — any other call panics, which is the
// point: a gated tick must touch nothing else.
type standbyFakeSettings struct {
	repository.ServerSettingsRepository
	s *models.ServerSettings
}

func (f *standbyFakeSettings) Get(context.Context) (*models.ServerSettings, error) {
	return f.s, nil
}

// standbyFakeSchedules panics on ListDue — a standby tick must never list
// due schedules.
type standbyFakeSchedules struct {
	repository.BackupScheduleRepository
}

// standbyFakeJobs panics on any use — a standby tick must never count or
// dispatch jobs.
type standbyFakeJobs struct {
	repository.BackupJobRepository
}

type standbyFakeDests struct{ repository.BackupDestinationRepository }
type standbyFakeUsers struct{ repository.UserRepository }

type standbyFakeAgent struct{}

func (standbyFakeAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	panic("standby tick must not call the agent")
}

// GH #331 two-node drill finding: the replicated primary DB carries the
// primary's ENABLED schedules (including the DR feed itself) and queued job
// rows. An ungated scheduler on a standby ran the feed schedule and shipped
// the STANDBY's state into the shared DR repo. Both tick halves must be
// inert on a standby.
func TestTickOnce_StandbyIsInert(t *testing.T) {
	s := New(Deps{
		Settings:     &standbyFakeSettings{s: &models.ServerSettings{ServerRole: models.ServerRoleStandby}},
		Schedules:    &standbyFakeSchedules{},
		Jobs:         &standbyFakeJobs{},
		Destinations: &standbyFakeDests{},
		Users:        &standbyFakeUsers{},
		Agent:        &standbyFakeAgent{},
		Log:          slog.Default(),
	})
	if s == nil {
		t.Fatal("New returned nil")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("standby tick touched schedules/jobs: %v", r)
		}
	}()
	s.TickOnce(context.Background())
}
