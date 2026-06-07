package cronvalidate

import (
	"fmt"
	"strconv"
	"strings"
)

// dowNames maps cron weekday number (0=Sun … 6=Sat) to the systemd
// OnCalendar weekday name. cron also allows 7 for Sunday.
var dowNames = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// CronToSystemdCalendar translates a validated 5-field POSIX cron
// expression ("min hour dom month dow") into a systemd OnCalendar=
// specification ("[DOW ]Y-Mon-Day H:Min:Sec").
//
// Why: systemd timers parse OnCalendar in systemd-calendar format, NOT
// cron — dumping "*/5 * * * *" into OnCalendar yields
// "Loaded: bad-setting" and the timer never fires. Callers must run the
// expression through ValidateSchedule first (this assumes 5 fields).
//
// Field translation per cron atom (each comma-separated piece):
//   *           -> *
//   N           -> N
//   */K         -> <fieldmin>/K            (systemd start/step; min 0 for
//                                            minute/hour, 1 for dom/month)
//   A-B         -> A..B
//   A-B/K       -> A..B/K
// dow numbers become names (7 -> Sun); month/day numbers pass through
// (systemd accepts numeric month + day). Seconds are pinned to :00.
func CronToSystemdCalendar(expr string) (string, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return "", fmt.Errorf("CronToSystemdCalendar: expected 5 cron fields, got %d (%q)", len(fields), expr)
	}
	minute := cronFieldToSystemd(fields[0], 0)
	hour := cronFieldToSystemd(fields[1], 0)
	dom := cronFieldToSystemd(fields[2], 1)
	month := cronFieldToSystemd(fields[3], 1)
	dow := cronDOWToSystemd(fields[4])

	cal := fmt.Sprintf("*-%s-%s %s:%s:00", month, dom, hour, minute)
	if dow != "" {
		cal = dow + " " + cal
	}
	return cal, nil
}

// cronFieldToSystemd converts a non-DOW cron field (which may be a
// comma list of atoms) to systemd syntax. fieldMin is the lowest legal
// value (0 for minute/hour, 1 for day-of-month/month) used to expand
// a bare */K step.
func cronFieldToSystemd(field string, fieldMin int) string {
	atoms := strings.Split(field, ",")
	for i, a := range atoms {
		atoms[i] = cronAtomToSystemd(a, fieldMin)
	}
	return strings.Join(atoms, ",")
}

func cronAtomToSystemd(atom string, fieldMin int) string {
	// */K  -> <min>/K
	if strings.HasPrefix(atom, "*/") {
		return fmt.Sprintf("%d/%s", fieldMin, atom[2:])
	}
	// A-B/K or A-B  -> A..B[/K]
	if slash := strings.IndexByte(atom, '/'); slash >= 0 {
		rng, step := atom[:slash], atom[slash+1:]
		rng = strings.Replace(rng, "-", "..", 1)
		return rng + "/" + step
	}
	if strings.Contains(atom, "-") {
		return strings.Replace(atom, "-", "..", 1)
	}
	// * or a bare number
	return atom
}

// cronDOWToSystemd converts the cron day-of-week field to a systemd
// weekday spec. Returns "" for "*" (systemd omits the weekday → every
// day). Numeric tokens become names; non-numeric (already a name) pass
// through.
func cronDOWToSystemd(field string) string {
	if field == "*" {
		return ""
	}
	atoms := strings.Split(field, ",")
	for i, a := range atoms {
		if slash := strings.IndexByte(a, '/'); slash >= 0 {
			rng, step := a[:slash], a[slash+1:]
			atoms[i] = cronDOWRange(rng) + "/" + step
			continue
		}
		atoms[i] = cronDOWRange(a)
	}
	return strings.Join(atoms, ",")
}

func cronDOWRange(s string) string {
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		return cronDOWName(s[:dash]) + ".." + cronDOWName(s[dash+1:])
	}
	return cronDOWName(s)
}

func cronDOWName(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return s // already a name (Mon, Tue, …)
	}
	if n == 7 {
		n = 0
	}
	if n >= 0 && n <= 6 {
		return dowNames[n]
	}
	return s
}
