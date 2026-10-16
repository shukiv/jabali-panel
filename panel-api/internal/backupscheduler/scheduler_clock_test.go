package backupscheduler

import (
	"context"
	"log/slog"
	"testing"
	"time"
	_ "time/tzdata"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/paneltime"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// clockFakeSchedules lists fixed rows and records UpdateNextRun calls; any
// other method panics through the nil embedded interface.
type clockFakeSchedules struct {
	repository.BackupScheduleRepository
	rows    []models.BackupSchedule
	updated map[string]time.Time
}

func (f *clockFakeSchedules) List(context.Context) ([]models.BackupSchedule, error) {
	return f.rows, nil
}

func (f *clockFakeSchedules) UpdateNextRun(_ context.Context, id string, next time.Time) error {
	if f.updated == nil {
		f.updated = map[string]time.Time{}
	}
	f.updated[id] = next
	return nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// next0300UTC is the next 03:00Z after now: what the UTC-only scheduler
// stored for "0 3 * * *".
func next0300UTC(t *testing.T, now time.Time) time.Time {
	t.Helper()
	next, err := time.Parse(time.RFC3339, now.Format("2006-01-02")+"T03:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// GH #2164: backup crons are read on the Server Settings timezone. Rows the
// UTC-only scheduler timed ("0 3 * * *" = 03:00Z) move to 03:00 server time on
// the first tick; overdue rows and window-governed rows are left alone, and a
// second tick changes nothing.
func TestSyncClock_RetimesCronSchedulesToThePanelZone(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	now := time.Now().UTC()
	sched := &clockFakeSchedules{rows: []models.BackupSchedule{
		{ID: "cron", CronExpr: "0 3 * * *", NextRunAt: ptrTime(next0300UTC(t, now))},
		{ID: "overdue", CronExpr: "0 3 * * *", NextRunAt: ptrTime(now.Add(-time.Hour))},
		{ID: "window", CronExpr: "0 3 * * *", Cadence: "daily", NextRunAt: ptrTime(now.Add(2 * time.Hour))},
		{ID: "never", CronExpr: "0 3 * * *"},
	}}
	s := &Scheduler{deps: Deps{
		Settings:  &standbyFakeSettings{s: &models.ServerSettings{Timezone: "Asia/Tokyo"}},
		Schedules: sched,
		Log:       slog.Default(),
	}}

	s.syncClock(context.Background())

	if paneltime.Name() != "Asia/Tokyo" {
		t.Fatalf("panel zone = %q, want Asia/Tokyo", paneltime.Name())
	}
	got, ok := sched.updated["cron"]
	if !ok {
		t.Fatalf("cron schedule not re-timed; updates = %v", sched.updated)
	}
	if lt := got.In(paneltime.Location()); lt.Hour() != 3 || lt.Minute() != 0 || !got.After(now) {
		t.Errorf("re-timed next run = %s (%s Tokyo), want the next 03:00 Tokyo after now", got, lt)
	}
	for _, id := range []string{"overdue", "window", "never"} {
		if _, ok := sched.updated[id]; ok {
			t.Errorf("schedule %q was re-timed; only future cron rows move", id)
		}
	}

	// Steady state: the rows now carry Tokyo-timed next runs.
	sched.rows[0].NextRunAt = ptrTime(got)
	sched.updated = nil
	s.syncClock(context.Background())
	if len(sched.updated) != 0 {
		t.Errorf("second tick re-timed %v; same zone must be a no-op", sched.updated)
	}
}

// An admin who changes the timezone moves the rows on the next tick.
func TestSyncClock_FollowsATimezoneChange(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	settings := &standbyFakeSettings{s: &models.ServerSettings{Timezone: "UTC"}}
	now := time.Now().UTC()
	sched := &clockFakeSchedules{rows: []models.BackupSchedule{{ID: "cron", CronExpr: "0 3 * * *", NextRunAt: ptrTime(next0300UTC(t, now))}}}
	s := &Scheduler{deps: Deps{Settings: settings, Schedules: sched, Log: slog.Default()}}

	s.syncClock(context.Background())
	if len(sched.updated) != 0 {
		t.Fatalf("UTC rows on a UTC panel re-timed: %v", sched.updated)
	}

	settings.s = &models.ServerSettings{Timezone: "America/New_York"}
	s.syncClock(context.Background())
	got, ok := sched.updated["cron"]
	if !ok {
		t.Fatal("timezone change did not re-time the cron schedule")
	}
	if lt := got.In(paneltime.Location()); lt.Hour() != 3 {
		t.Errorf("re-timed next run = %s (%s New York), want 03:00 New York", got, lt)
	}
}
