package backup

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UTC()
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"02:00", 120, true},
		{"00:00", 0, true},
		{"23:59", 23*60 + 59, true},
		{" 05:30 ", 5*60 + 30, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"noon", 0, false},
		{"12", 0, false},
	}
	for _, c := range cases {
		got, ok := parseHHMM(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseHHMM(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseWindowFallsBackToDefault(t *testing.T) {
	if w := ParseWindow("garbage", "05:00"); w != DefaultTenantWindow {
		t.Errorf("bad start should fall back to default, got %+v", w)
	}
	if w := ParseWindow("02:00", ""); w != DefaultTenantWindow {
		t.Errorf("bad end should fall back to default, got %+v", w)
	}
	if w := ParseWindow("01:15", "06:45"); w.StartMin != 75 || w.EndMin != 405 {
		t.Errorf("good values should parse, got %+v", w)
	}
}

func TestWindowContains(t *testing.T) {
	w := TenantWindow{StartMin: 120, EndMin: 300} // 02:00–05:00
	if !w.Contains(mustTime(t, "2026-08-05T02:00:00Z")) {
		t.Error("start is inclusive")
	}
	if !w.Contains(mustTime(t, "2026-08-05T04:59:00Z")) {
		t.Error("04:59 inside")
	}
	if w.Contains(mustTime(t, "2026-08-05T05:00:00Z")) {
		t.Error("end is exclusive")
	}
	if w.Contains(mustTime(t, "2026-08-05T01:59:00Z")) {
		t.Error("01:59 outside")
	}
}

func TestWindowContainsWrapAndWholeDay(t *testing.T) {
	wrap := TenantWindow{StartMin: 22 * 60, EndMin: 4 * 60} // 22:00–04:00
	if !wrap.Contains(mustTime(t, "2026-08-05T23:30:00Z")) {
		t.Error("23:30 inside wrap window")
	}
	if !wrap.Contains(mustTime(t, "2026-08-05T03:00:00Z")) {
		t.Error("03:00 inside wrap window")
	}
	if wrap.Contains(mustTime(t, "2026-08-05T12:00:00Z")) {
		t.Error("noon outside wrap window")
	}
	whole := TenantWindow{StartMin: 0, EndMin: 0}
	if !whole.Contains(mustTime(t, "2026-08-05T13:37:00Z")) {
		t.Error("whole-day window contains any instant")
	}
}

func TestWindowNextOpen(t *testing.T) {
	w := TenantWindow{StartMin: 120, EndMin: 300}
	// Before today's window → today's open.
	if got := w.NextOpen(mustTime(t, "2026-08-05T00:30:00Z")); !got.Equal(mustTime(t, "2026-08-05T02:00:00Z")) {
		t.Errorf("before window: got %s", got)
	}
	// After today's window → tomorrow's open.
	if got := w.NextOpen(mustTime(t, "2026-08-05T09:00:00Z")); !got.Equal(mustTime(t, "2026-08-06T02:00:00Z")) {
		t.Errorf("after window: got %s", got)
	}
	// Inside window → unchanged (to the minute).
	if got := w.NextOpen(mustTime(t, "2026-08-05T03:15:30Z")); !got.Equal(mustTime(t, "2026-08-05T03:15:00Z")) {
		t.Errorf("inside window: got %s", got)
	}
}

func TestValidCadenceAndInterval(t *testing.T) {
	for _, c := range []string{CadenceHourly, CadenceEvery6h, CadenceEvery12h, CadenceDaily, CadenceWeekly} {
		if !ValidCadence(c) {
			t.Errorf("%q should be valid", c)
		}
	}
	// Empty is the legacy-cron discriminator and must NOT be a valid tenant cadence.
	if ValidCadence("") || ValidCadence("monthly") {
		t.Error("empty/unknown cadence must be invalid")
	}
	if CadenceInterval(CadenceHourly) != time.Hour {
		t.Error("hourly interval")
	}
	if CadenceInterval("nonsense") != 24*time.Hour {
		t.Error("unknown cadence must fall back to daily/24h")
	}
}

func TestNextTenantRunInWindowKeepsInterval(t *testing.T) {
	w := TenantWindow{StartMin: 120, EndMin: 300} // 02:00–05:00
	// Fired at 02:12 inside the window; hourly → 03:12, still inside → no re-smear.
	got := NextTenantRun(mustTime(t, "2026-08-05T02:12:00Z"), CadenceHourly, w, "sched-A")
	if !got.Equal(mustTime(t, "2026-08-05T03:12:00Z")) {
		t.Errorf("hourly in-window should be +1h same minute, got %s", got)
	}
}

// TestNextTenantRunUngatedFiresEveryInterval pins GH #1097: with the window
// disabled (whole-day, StartMin==EndMin — what the scheduler passes when
// tenant_backup_window_enforce is off) every cadence fires on its plain
// interval from now, no window gating, no smear. Hourly = every hour.
func TestNextTenantRunUngatedFiresEveryInterval(t *testing.T) {
	ungated := TenantWindow{} // whole-day
	// A time deliberately OUTSIDE the old 02:00–05:00 window — with gating off it
	// must not be deferred to a window opening.
	now := mustTime(t, "2026-08-05T14:37:00Z")
	cases := []struct {
		cadence string
		want    string
	}{
		{CadenceHourly, "2026-08-05T15:37:00Z"},
		{CadenceEvery6h, "2026-08-05T20:37:00Z"},
		{CadenceEvery12h, "2026-08-06T02:37:00Z"},
		{CadenceDaily, "2026-08-06T14:37:00Z"},
	}
	for _, c := range cases {
		got := NextTenantRun(now, c.cadence, ungated, "sched-A")
		if !got.Equal(mustTime(t, c.want)) {
			t.Errorf("%s ungated: want %s, got %s", c.cadence, c.want, got)
		}
	}
}

func TestNextTenantRunOutOfWindowJumpsToOpenPlusSmear(t *testing.T) {
	w := TenantWindow{StartMin: 120, EndMin: 300}
	// Fired at 04:30 → +1h = 05:30 outside → next open tomorrow 02:00 + smear.
	got := NextTenantRun(mustTime(t, "2026-08-05T04:30:00Z"), CadenceHourly, w, "sched-A")
	open := mustTime(t, "2026-08-06T02:00:00Z")
	if got.Before(open) || !w.Contains(got) {
		t.Errorf("out-of-window fire must land in the next window, got %s", got)
	}
	if got.Sub(open) >= time.Hour {
		t.Errorf("hourly smear must be < 1h into the window, got +%s", got.Sub(open))
	}
}

func TestNextTenantRunSmearIsDeterministicAndPerSchedule(t *testing.T) {
	w := TenantWindow{StartMin: 120, EndMin: 300}
	now := mustTime(t, "2026-08-05T09:00:00Z") // well outside window
	a1 := NextTenantRun(now, CadenceDaily, w, "sched-A")
	a2 := NextTenantRun(now, CadenceDaily, w, "sched-A")
	b := NextTenantRun(now, CadenceDaily, w, "sched-B")
	if !a1.Equal(a2) {
		t.Errorf("same schedule id must be deterministic: %s vs %s", a1, a2)
	}
	if !w.Contains(a1) || !w.Contains(b) {
		t.Errorf("daily fires must land in the window: A=%s B=%s", a1, b)
	}
	// Different ids almost certainly get different smeared minutes across a
	// 180-minute window; assert they are not identical (guards the per-schedule
	// spreading that prevents a stampede).
	if a1.Equal(b) {
		t.Errorf("distinct schedule ids should smear to distinct minutes, both %s", a1)
	}
}
