package backup

import "time"

// An account's "Days to keep" keeps every backup of the last N days (GH
// #2300), so how many backups that is depends on how often the schedule runs.
// The account may keep as many days as fit in its plan's backup limit.
// panel-ui/src/lib/backupScheduleOptions.ts has the same table and rule; a
// test keeps them equal.

// CadenceRunsPerWeek is how many backups a cadence makes in a week. An unknown
// cadence counts as daily, as CadenceInterval does.
func CadenceRunsPerWeek(c string) int {
	switch c {
	case CadenceHourly:
		return 168
	case CadenceEvery6h:
		return 28
	case CadenceEvery12h:
		return 14
	case CadenceWeekly:
		return 1
	}
	return 7
}

// CronRunsPerWeek counts the times a cron expression fires in the week after
// from, (from, from+7d]: the backups a cron-timed schedule makes in a week.
func CronRunsPerWeek(expr string, from time.Time) (int, error) {
	end := from.AddDate(0, 0, 7)
	n := 0
	for t := from; n < 7*24*60; n++ {
		next, err := NextFire(expr, t)
		if err != nil {
			return 0, err
		}
		if next.After(end) {
			break
		}
		t = next
	}
	return n, nil
}

// MaxKeepWithinDays is the most days to keep whose backups fit in a plan's
// backup limit at runsPerWeek, and at least 1: an hourly schedule on a plan of
// 7 backups still keeps a day, and the scheduler then removes its oldest
// backup at the limit.
func MaxKeepWithinDays(maxBackups uint32, runsPerWeek int) int {
	if runsPerWeek <= 0 {
		runsPerWeek = 7
	}
	if d := int(maxBackups) * 7 / runsPerWeek; d > 1 {
		return d
	}
	return 1
}
