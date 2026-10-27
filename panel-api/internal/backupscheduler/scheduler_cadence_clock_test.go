package backupscheduler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// cadenceFakeSchedules has no destinations for any schedule, so a due tenant
// schedule enqueues nothing and only its next run is stored.
type cadenceFakeSchedules struct {
	clockFakeSchedules
}

func (f *cadenceFakeSchedules) GetDestinations(context.Context, string) ([]models.BackupDestination, error) {
	return nil, nil
}

// GH #2300: a tenant schedule that the tick sees late (the panel was
// restarting) keeps its times: the next run is the planned one plus the
// interval, not the interval counted from the late tick.
func TestEnqueueTenantWindow_LateTickKeepsThePlannedTimes(t *testing.T) {
	now := time.Now().UTC()
	planned := now.Add(-2 * time.Hour).Truncate(time.Minute)
	sched := &cadenceFakeSchedules{}
	s := &Scheduler{deps: Deps{
		Settings:  &standbyFakeSettings{s: &models.ServerSettings{}}, // window not enforced
		Schedules: sched,
		Log:       slog.Default(),
	}}

	s.enqueueTenantWindow(context.Background(), models.BackupSchedule{
		ID: "s1", Kind: models.BackupScheduleKindAccount, Cadence: "every_6h", NextRunAt: &planned,
	})

	got, ok := sched.updated["s1"]
	if !ok {
		t.Fatal("next run not stored")
	}
	if want := planned.Add(6 * time.Hour); !got.Equal(want) {
		t.Errorf("next run = %s, want %s (planned + 6 h), not 6 h after the late tick", got, want)
	}
}
