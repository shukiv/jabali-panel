// Package backup — tenant multi-schedule window model (GH #454 Phase 7B).
//
// The admin owns a maintenance WINDOW (server_settings.tenant_backup_window_*)
// and tenants own a CADENCE per schedule. A window-governed schedule fires only
// while the clock is inside the window, and a deterministic per-schedule offset
// smears the firings across the window so N tenants that come due together do
// not all hit window-open at once. This keeps the resource-planning guarantee
// the single-slot model gave the admin while letting a tenant run several
// differently-scoped backups.
//
// The window is minute-of-day on the panel's clock (GH #2164: the Server
// Settings timezone, the time the panel shows; it was UTC before). Instants in
// and out stay UTC. Cadence is a fixed allow-list mapped to an interval (never
// free cron), so a tenant can never choose a stampede minute.
package backup

import (
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/paneltime"
)

// Tenant backup cadences (GH #454 7B). Each maps to a fixed interval; the window
// caps how many of those interval firings actually run per day.
const (
	CadenceHourly   = "hourly"
	CadenceEvery6h  = "every_6h"
	CadenceEvery12h = "every_12h"
	CadenceDaily    = "daily"
	CadenceWeekly   = "weekly"
)

// ValidCadence reports whether c is an accepted tenant cadence. The empty string
// is NOT valid here — it is the discriminator for a legacy cron-governed
// schedule and must never be accepted from the tenant multi-schedule surface.
func ValidCadence(c string) bool {
	switch c {
	case CadenceHourly, CadenceEvery6h, CadenceEvery12h, CadenceDaily, CadenceWeekly:
		return true
	}
	return false
}

// CadenceInterval maps a cadence to its firing interval. An unknown cadence
// falls back to daily (24h) — the safest, least-frequent choice — so a bad
// stored value can never produce a tight loop.
func CadenceInterval(c string) time.Duration {
	switch c {
	case CadenceHourly:
		return time.Hour
	case CadenceEvery6h:
		return 6 * time.Hour
	case CadenceEvery12h:
		return 12 * time.Hour
	case CadenceWeekly:
		return 7 * 24 * time.Hour
	default: // daily + unknown
		return 24 * time.Hour
	}
}

// TenantWindow is the admin maintenance window as minutes since midnight on the
// panel's clock, both in [0,1440). StartMin == EndMin means the window spans the
// whole day (no gating). StartMin > EndMin is a wrap-around window (e.g.
// 22:00–04:00).
type TenantWindow struct {
	StartMin int
	EndMin   int
	// Loc is the clock the minutes are read on. nil = the panel's clock
	// (paneltime.Location()).
	Loc *time.Location
}

// clock is the zone the window's minutes are read in.
func (w TenantWindow) clock() *time.Location {
	if w.Loc != nil {
		return w.Loc
	}
	return paneltime.Location()
}

// DefaultTenantWindow matches the migration column defaults (02:00–05:00 server
// time).
// Returned whenever a stored window value can't be parsed, so a corrupt settings
// row degrades to a safe gated window rather than to "no window / fire anytime".
var DefaultTenantWindow = TenantWindow{StartMin: 2 * 60, EndMin: 5 * 60}

// ParseWindow builds a TenantWindow from two "HH:MM" strings. Any parse error in
// either bound falls back to DefaultTenantWindow — never to an ungated window.
func ParseWindow(start, end string) TenantWindow {
	s, sok := parseHHMM(start)
	e, eok := parseHHMM(end)
	if !sok || !eok {
		return DefaultTenantWindow
	}
	return TenantWindow{StartMin: s, EndMin: e}
}

// ValidHHMM reports whether s is a well-formed 24h "HH:MM" clock string. Used by
// the admin settings validator to reject a bad window bound instead of silently
// falling back to the default (which ParseWindow does at read time).
func ValidHHMM(s string) bool {
	_, ok := parseHHMM(s)
	return ok
}

// parseHHMM parses "HH:MM" (24h) into minutes since midnight in [0,1440).
func parseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, herr := strconv.Atoi(parts[0])
	m, merr := strconv.Atoi(parts[1])
	if herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// spanMinutes is the window length. A whole-day window (start == end) is 1440.
func (w TenantWindow) spanMinutes() int {
	if w.StartMin == w.EndMin {
		return 24 * 60
	}
	if w.EndMin > w.StartMin {
		return w.EndMin - w.StartMin
	}
	return (24 * 60) - w.StartMin + w.EndMin // wrap-around
}

// Contains reports whether t's minute-of-day on the window's clock is inside the
// window. The start is inclusive, the end exclusive. A whole-day window contains
// every instant.
func (w TenantWindow) Contains(t time.Time) bool {
	if w.StartMin == w.EndMin {
		return true
	}
	lt := t.In(w.clock())
	m := lt.Hour()*60 + lt.Minute()
	if w.EndMin > w.StartMin {
		return m >= w.StartMin && m < w.EndMin
	}
	// wrap-around: inside if at/after start OR before end
	return m >= w.StartMin || m < w.EndMin
}

// NextOpen returns the earliest instant >= t at which the window is open, in
// UTC. When t is already inside the window it returns t unchanged (truncated to
// the minute).
func (w TenantWindow) NextOpen(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute)
	if w.Contains(t) {
		return t
	}
	// Candidate = today's start boundary on the window's clock; roll forward a
	// calendar day until it is >= t. time.Date per day, not +24h, so a DST
	// change keeps the opening at the same wall-clock minute. Tomorrow's opening
	// is always >= t; the third day is only a backstop.
	lt := t.In(w.clock())
	for d := 0; d < 2; d++ {
		if open := w.openOn(lt, d); !open.Before(t) {
			return open
		}
	}
	return w.openOn(lt, 2)
}

// openOn is the window's start on the calendar day `days` after lt's, in UTC.
func (w TenantWindow) openOn(lt time.Time, days int) time.Time {
	return time.Date(lt.Year(), lt.Month(), lt.Day()+days, w.StartMin/60, w.StartMin%60, 0, 0, w.clock()).UTC()
}

// smearOffset returns a deterministic per-schedule offset in [0, capMinutes)
// minutes, derived from the schedule ID via FNV-1a. Distinct schedule IDs get
// distinct offsets, so schedules that come due together spread across the
// window instead of stampeding window-open. Deterministic (not random) so a
// schedule's smeared minute stays stable across restarts and resumes.
func smearOffset(scheduleID string, capMinutes int) time.Duration {
	if capMinutes <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(scheduleID))
	return time.Duration(int(h.Sum32()%uint32(capMinutes))) * time.Minute
}

// NextTenantRun computes the next firing time (strictly after now) for a
// window-governed tenant schedule. Rules:
//   - one cadence interval out from now;
//   - if that lands inside the window, keep it (interval firings inside the
//     window preserve the schedule's smeared minute — no re-smear, no drift);
//   - if it lands outside, jump to the next window opening and add the
//     schedule's deterministic smear offset (bounded to the window span so it
//     never lands past window-close).
//
// The smear cap is min(interval, windowSpan): capping at the interval keeps a
// short-cadence schedule (hourly) smeared within its own hour; capping at the
// window span keeps a long-cadence schedule spread across the whole window.
func NextTenantRun(now time.Time, cadence string, w TenantWindow, scheduleID string) time.Time {
	interval := CadenceInterval(cadence)
	cand := now.UTC().Add(interval).Truncate(time.Minute)
	if w.Contains(cand) {
		return cand
	}
	open := w.NextOpen(cand)
	capMin := int(interval / time.Minute)
	if span := w.spanMinutes(); span < capMin {
		capMin = span
	}
	smeared := open.Add(smearOffset(scheduleID, capMin))
	if !w.Contains(smeared) {
		return open
	}
	return smeared
}
