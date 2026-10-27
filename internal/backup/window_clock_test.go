package backup

import (
	"testing"
	"time"
	_ "time/tzdata" // Europe/Berlin loads on a host without tzdata too
)

// GH #2300: a tenant schedule's runs keep the clock times of its planned run,
// like cron, through a DST change, a run that starts late, and runs missed
// while the panel was down.

func berlinClock(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// tick is how late the scheduler's 60 s tick sees a due run.
const tick = 40 * time.Second

// Every 6 hours from 20:29 CEST on 24 Oct stays at 20:29 after the clocks go
// back on 25 Oct (2026). A fixed 6-hour gap lands on 19:29 CET, the shift the
// reporter saw.
func TestNextTenantRunAfter_KeepsClockTimesAcrossDSTFallBack(t *testing.T) {
	loc := berlinClock(t)
	w := TenantWindow{Loc: loc} // whole day: no gating
	run := time.Date(2026, 10, 24, 20, 29, 0, 0, loc).UTC()
	var got []time.Time
	for i := 0; i < 4; i++ {
		run = NextTenantRunAfter(run, run.Add(tick), CadenceEvery6h, w, "sched-A")
		got = append(got, run)
	}
	for i, r := range got {
		if l := r.In(loc); l.Hour()%6 != 2 || l.Minute() != 29 {
			t.Errorf("run %d at %s, want one of 02:29, 08:29, 14:29, 20:29 Berlin", i+1, l)
		}
	}
	if want := time.Date(2026, 10, 25, 20, 29, 0, 0, loc).UTC(); !got[3].Equal(want) {
		t.Errorf("fourth run = %s, want %s (20:29 CET)", got[3].In(loc), want.In(loc))
	}
}

// The scheduler and the handlers pass TenantWindow{} (no window, no Loc): the
// hours are read on the panel's clock, the Server Settings timezone.
func TestNextTenantRunAfter_ReadsThePanelClock(t *testing.T) {
	setPanelZone(t, "Europe/Berlin")
	loc := berlinClock(t)
	planned := time.Date(2026, 10, 24, 20, 29, 0, 0, loc).UTC()
	got := NextTenantRunAfter(planned, time.Date(2026, 10, 25, 19, 0, 0, 0, loc).UTC(), CadenceEvery6h, TenantWindow{}, "sched-A")
	if want := time.Date(2026, 10, 25, 20, 29, 0, 0, loc).UTC(); !got.Equal(want) {
		t.Errorf("got %s, want %s (20:29 on the panel clock)", got.In(loc), want.In(loc))
	}
}

// 02:29 happens twice on 25 Oct in Berlin. Whichever of the two the series
// runs at, the next run is 08:29 CET: it doesn't run a second time at 02:29.
func TestNextTenantRunAfter_RepeatedHourRunsOnce(t *testing.T) {
	loc := berlinClock(t)
	w := TenantWindow{Loc: loc}
	for _, planned := range []string{"2026-10-25T00:29:00Z", "2026-10-25T01:29:00Z"} { // 02:29 CEST, 02:29 CET
		p := mustTime(t, planned)
		got := NextTenantRunAfter(p, p.Add(tick), CadenceEvery6h, w, "sched-A")
		if want := mustTime(t, "2026-10-25T07:29:00Z"); !got.Equal(want) {
			t.Errorf("from %s: got %s, want %s (08:29 CET)", p.In(loc), got.In(loc), want.In(loc))
		}
	}
}

// Daily and weekly runs keep their clock time across the change too.
func TestNextTenantRunAfter_DailyAndWeeklyKeepTheirClockTime(t *testing.T) {
	loc := berlinClock(t)
	w := TenantWindow{Loc: loc}
	cases := []struct {
		cadence       string
		planned, want time.Time
	}{
		{CadenceDaily, time.Date(2026, 10, 24, 23, 3, 0, 0, loc), time.Date(2026, 10, 25, 23, 3, 0, 0, loc)},
		{CadenceEvery12h, time.Date(2026, 10, 24, 20, 30, 0, 0, loc), time.Date(2026, 10, 25, 8, 30, 0, 0, loc)},
		{CadenceWeekly, time.Date(2026, 10, 22, 20, 30, 0, 0, loc), time.Date(2026, 10, 29, 20, 30, 0, 0, loc)},
	}
	for _, c := range cases {
		got := NextTenantRunAfter(c.planned.UTC(), c.planned.UTC().Add(tick), c.cadence, w, "sched-A")
		if !got.Equal(c.want.UTC()) {
			t.Errorf("%s from %s: got %s, want %s", c.cadence, c.planned, got.In(loc), c.want)
		}
	}
}

// A run that starts late (the panel was restarting at 19:29) doesn't move the
// ones after it: the next is still 01:29, not 6 hours after the late start.
func TestNextTenantRunAfter_LateRunKeepsTheTimes(t *testing.T) {
	loc := berlinClock(t)
	w := TenantWindow{Loc: loc}
	planned := time.Date(2026, 10, 26, 19, 29, 0, 0, loc).UTC()
	now := time.Date(2026, 10, 26, 21, 0, 0, 0, loc).UTC()
	got := NextTenantRunAfter(planned, now, CadenceEvery6h, w, "sched-A")
	if want := time.Date(2026, 10, 27, 1, 29, 0, 0, loc).UTC(); !got.Equal(want) {
		t.Errorf("got %s, want %s", got.In(loc), want.In(loc))
	}
}

// Runs missed while the panel was down for days: the overdue one runs once,
// and the next is the next clock time after now, not a run per missed slot.
func TestNextTenantRunAfter_MissedRunsDontPileUp(t *testing.T) {
	loc := berlinClock(t)
	w := TenantWindow{Loc: loc}
	planned := time.Date(2026, 10, 20, 19, 29, 0, 0, loc).UTC()
	now := time.Date(2026, 10, 23, 10, 12, 0, 0, loc).UTC()
	got := NextTenantRunAfter(planned, now, CadenceEvery6h, w, "sched-A")
	if want := time.Date(2026, 10, 23, 13, 29, 0, 0, loc).UTC(); !got.Equal(want) {
		t.Errorf("got %s, want %s", got.In(loc), want.In(loc))
	}
}

// A clock time the change to summer time skips (02:29 on 29 Mar 2026 in
// Berlin) is skipped, as cron does, and the runs after it keep their times.
func TestNextTenantRunAfter_SkipsAClockTimeSummerTimeSkips(t *testing.T) {
	loc := berlinClock(t)
	w := TenantWindow{Loc: loc}
	cases := []struct {
		cadence       string
		planned, want time.Time
	}{
		{CadenceEvery6h, time.Date(2026, 3, 28, 20, 29, 0, 0, loc), time.Date(2026, 3, 29, 8, 29, 0, 0, loc)},
		{CadenceDaily, time.Date(2026, 3, 28, 2, 29, 0, 0, loc), time.Date(2026, 3, 30, 2, 29, 0, 0, loc)},
	}
	for _, c := range cases {
		got := NextTenantRunAfter(c.planned.UTC(), c.planned.UTC().Add(tick), c.cadence, w, "sched-A")
		if !got.Equal(c.want.UTC()) {
			t.Errorf("%s from %s: got %s, want %s", c.cadence, c.planned, got.In(loc), c.want)
		}
	}
	// And the series is back on 02:29 the next night.
	next := NextTenantRunAfter(time.Date(2026, 3, 29, 20, 29, 0, 0, loc).UTC(),
		time.Date(2026, 3, 29, 20, 29, 0, 0, loc).UTC().Add(tick), CadenceEvery6h, w, "sched-A")
	if want := time.Date(2026, 3, 30, 2, 29, 0, 0, loc).UTC(); !next.Equal(want) {
		t.Errorf("after the change: got %s, want %s", next.In(loc), want.In(loc))
	}
}

// Hourly keeps the planned minute, counted in elapsed hours, so the hour the
// clocks go back is neither skipped nor run twice.
func TestNextTenantRunAfter_HourlyKeepsTheMinute(t *testing.T) {
	w := TenantWindow{}
	got := NextTenantRunAfter(mustTime(t, "2026-08-05T10:05:00Z"), mustTime(t, "2026-08-05T13:40:00Z"), CadenceHourly, w, "sched-A")
	if want := mustTime(t, "2026-08-05T14:05:00Z"); !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
	// A planned run still ahead: the first of its hourly times after now.
	got = NextTenantRunAfter(mustTime(t, "2026-08-05T16:05:00Z"), mustTime(t, "2026-08-05T13:40:00Z"), CadenceHourly, w, "sched-A")
	if want := mustTime(t, "2026-08-05T14:05:00Z"); !got.Equal(want) {
		t.Errorf("planned ahead: got %s, want %s", got, want)
	}
	// 02:05 CEST → 02:05 CET, an hour later.
	loc := berlinClock(t)
	got = NextTenantRunAfter(mustTime(t, "2026-10-25T00:05:00Z"), mustTime(t, "2026-10-25T00:05:40Z"), CadenceHourly, TenantWindow{Loc: loc}, "sched-A")
	if want := mustTime(t, "2026-10-25T01:05:00Z"); !got.Equal(want) {
		t.Errorf("hourly over the change: got %s, want %s", got, want)
	}
}

// With the admin's window enforced, runs keep the window rules NextTenantRun
// has: inside the window the next interval, outside it the next opening plus
// the schedule's offset.
func TestNextTenantRunAfter_EnforcedWindowKeepsItsRules(t *testing.T) {
	w := TenantWindow{StartMin: 120, EndMin: 300} // 02:00–05:00 UTC
	got := NextTenantRunAfter(mustTime(t, "2026-08-05T02:17:00Z"), mustTime(t, "2026-08-05T02:17:40Z"), CadenceHourly, w, "sched-A")
	if want := mustTime(t, "2026-08-05T03:17:00Z"); !got.Equal(want) {
		t.Errorf("in window: got %s, want %s", got, want)
	}
	planned := mustTime(t, "2026-08-05T04:17:00Z")
	got = NextTenantRunAfter(planned, planned.Add(tick), CadenceHourly, w, "sched-A")
	if want := NextTenantRun(planned, CadenceHourly, w, "sched-A"); !got.Equal(want) {
		t.Errorf("past the window: got %s, want %s (next opening plus offset)", got, want)
	}
	// A daily run at its offset stays there.
	daily := NextTenantRun(mustTime(t, "2026-08-05T09:00:00Z"), CadenceDaily, w, "sched-A")
	got = NextTenantRunAfter(daily, daily.Add(tick), CadenceDaily, w, "sched-A")
	if want := daily.Add(24 * time.Hour); !got.Equal(want) {
		t.Errorf("daily in window: got %s, want %s", got, want)
	}
}

// No planned run (an old row): the next run is one interval from now.
func TestNextTenantRunAfter_NoPlannedRun(t *testing.T) {
	now := mustTime(t, "2026-08-05T14:37:00Z")
	got := NextTenantRunAfter(time.Time{}, now, CadenceEvery6h, TenantWindow{}, "sched-A")
	if want := NextTenantRun(now, CadenceEvery6h, TenantWindow{}, "sched-A"); !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}
