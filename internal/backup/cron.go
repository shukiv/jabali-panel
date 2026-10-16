// Package backup — cron parsing for the M30.1 scheduler. Standard
// 5-field cron (no seconds, no @yearly etc.) keeps the UI simple and
// matches the presets emitted by the admin form (Daily/Weekly/Monthly).
//
// Errors here are surfaced to the REST validator so the admin sees a
// clean "invalid cron expression" rather than a 500.
package backup

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/paneltime"
)

// CronParser is the package-level parser. Standard parser = minute,
// hour, day-of-month, month, day-of-week. No seconds, no descriptors.
var CronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// PresetCronExpr maps the UI preset names to canonical cron strings.
// "custom" returns the empty string — caller must look at the raw
// cron_expr field instead.
var PresetCronExpr = map[string]string{
	"daily":   "0 3 * * *", // every day at 03:00 (server time, GH #2164)
	"weekly":  "0 3 * * 0", // Sunday at 03:00
	"monthly": "0 3 1 * *", // 1st of month at 03:00
}

// ParseCron returns a robfig schedule or a wrapped error.
func ParseCron(expr string) (cron.Schedule, error) {
	sched, err := CronParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return sched, nil
}

// NextFire computes the next firing strictly after `from`. The robfig
// parser already enforces the "strictly after" semantics.
//
// GH #2164: the cron is read on the panel's clock (paneltime.Location(),
// the Server Settings timezone), so "0 3 * * *" means 03:00 server time,
// the time the panel shows. The result is UTC, for the database and the
// API.
func NextFire(expr string, from time.Time) (time.Time, error) {
	return NextFireIn(expr, from, paneltime.Location())
}

// NextFireIn is NextFire on an explicit clock.
func NextFireIn(expr string, from time.Time, loc *time.Location) (time.Time, error) {
	sched, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from.In(loc)).UTC(), nil
}

// PreviewFires returns the next n firings strictly after `from`, read on
// the panel's clock like NextFire and returned in UTC. The UI uses this
// for the "next 5 firings" preview on the schedule drawer. Bounded by n;
// callers cap at a sane number (5-10).
func PreviewFires(expr string, from time.Time, n int) ([]time.Time, error) {
	if n <= 0 {
		return nil, nil
	}
	sched, err := ParseCron(expr)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, n)
	cursor := from.In(paneltime.Location())
	for i := 0; i < n; i++ {
		next := sched.Next(cursor)
		out = append(out, next.UTC())
		cursor = next
	}
	return out, nil
}
