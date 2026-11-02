package main

// GH #2300: an account's "Days to keep" keeps every backup from the last N
// days (keep_within_days), not the newest one of each day. A schedule that
// runs every 6 hours with 7 days to keep holds 28 restore points, not 7.

import (
	"fmt"
	"testing"
	"time"
)

// sixHourly makes n complete backups every 6 hours from start, oldest first,
// named j00, j01, ….
func sixHourly(start time.Time, n int) []resticSnapshot {
	var snaps []resticSnapshot
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * 6 * time.Hour).Format(time.RFC3339)
		snaps = append(snaps, jobSet(fmt.Sprintf("j%02d", i), "u1", at, at, at)...)
	}
	return snaps
}

func TestPlanJobRetention_WithinDays_KeepsEveryBackupOfTheLastNDays(t *testing.T) {
	// 8 days of a 6-hourly schedule: 2026-10-20 01:29 to 2026-10-27 19:29.
	snaps := sixHourly(time.Date(2026, 10, 20, 1, 29, 0, 0, time.UTC), 32)
	plan := planJobRetention(snaps, retentionPolicy{WithinDays: 7})

	if len(plan.Keep) != 28 {
		t.Fatalf("kept %d backups, want 28 (4 a day for 7 days): %v", len(plan.Keep), jobIDs(plan.Keep))
	}
	// j03 (2026-10-20 19:29) is exactly 7 days before the newest: out.
	if want := []string{"j00", "j01", "j02", "j03"}; !eq(jobIDs(plan.Drop), want) {
		t.Errorf("dropped %v, want %v", jobIDs(plan.Drop), want)
	}
}

// The window is counted from the newest COMPLETE backup, so a string of
// failed runs (data but no manifest) can't push complete ones out of it.
func TestPlanJobRetention_WithinDays_CountsFromNewestCompleteBackup(t *testing.T) {
	snaps := sixHourly(time.Date(2026, 10, 20, 1, 29, 0, 0, time.UTC), 8) // to 10-21 19:29
	for i, at := range []string{"2026-10-23T01:29:00Z", "2026-10-24T01:29:00Z", "2026-10-25T01:29:00Z"} {
		snaps = append(snaps, snap(fmt.Sprintf("f%d-home", i), fmt.Sprintf("f%d", i), "u1", "home", at))
	}
	plan := planJobRetention(snaps, retentionPolicy{WithinDays: 1})

	// Newest complete: j07 at 10-21 19:29, so 1 day keeps j04..j07, and the
	// newer incomplete runs are inside the window too.
	want := []string{"f0", "f1", "f2", "j04", "j05", "j06", "j07"}
	if !eq(jobIDs(plan.Keep), want) {
		t.Errorf("kept %v, want %v", jobIDs(plan.Keep), want)
	}
}

// Days to keep adds to the other keep counts: a backup kept by any rule stays.
func TestPlanJobRetention_WithinDays_AddsToDailyRule(t *testing.T) {
	snaps := sixHourly(time.Date(2026, 10, 20, 1, 29, 0, 0, time.UTC), 16) // 4 days
	plan := planJobRetention(snaps, retentionPolicy{WithinDays: 1, Daily: 3})

	// Last day: j12..j15. Daily 3: newest of 10-23 (j15), 10-22 (j11), 10-21 (j07).
	want := []string{"j07", "j11", "j12", "j13", "j14", "j15"}
	if !eq(jobIDs(plan.Keep), want) {
		t.Errorf("kept %v, want %v", jobIDs(plan.Keep), want)
	}
}

func TestRetentionPolicy_WithinDaysAloneIsAPolicy(t *testing.T) {
	if (retentionPolicy{WithinDays: 3}).empty() {
		t.Error("a policy with only days to keep must not be empty, or the sweep skips it")
	}
}
