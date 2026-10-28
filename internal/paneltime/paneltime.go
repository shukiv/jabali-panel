// Package paneltime is the panel's clock for people (GH #2164): the timezone
// the panel shows times in and evaluates backup schedules in, and the date
// format it shows them with. Both come from Server Settings → Server Time.
//
// The zone resolves the Server Settings timezone first, then the OS zone, then
// UTC. It is held process-wide because it is process-wide state, the same way
// time.Local is: the panel boot, the settings PATCH and the backup scheduler's
// tick keep it current, and every cron evaluation reads it. APIs, logs and the
// database stay UTC; this package only decides how a UTC instant is read.
package paneltime

import (
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultDateFormat is the format a fresh install shows ("16/10/26 06:36").
// It is the migration's column default too.
const DefaultDateFormat = "DD/MM/YY HH:mm"

// DateFormats is the closed list an admin can pick from, as dayjs patterns for
// a date with a time. The SPA keeps the matching presets (with their seconds
// and date-only forms) in panel-ui/src/utils/dateFormats.ts; a test keeps the
// two lists equal.
var DateFormats = []string{
	"DD/MM/YY HH:mm",
	"DD/MM/YYYY HH:mm",
	"DD.MM.YYYY HH:mm",
	"YYYY-MM-DD HH:mm",
	"MM/DD/YYYY hh:mm A",
	"DD MMM YYYY, HH:mm",
}

// ValidDateFormat reports whether f is one of DateFormats.
func ValidDateFormat(f string) bool {
	for _, d := range DateFormats {
		if f == d {
			return true
		}
	}
	return false
}

// EffectiveDateFormat is the format to show for a stored value: the value when
// it is one of DateFormats, otherwise the default. A value from an older or
// newer release that this one does not know never reaches the SPA.
func EffectiveDateFormat(stored string) string {
	if ValidDateFormat(stored) {
		return stored
	}
	return DefaultDateFormat
}

// The OS zone files, variables so tests can point them elsewhere.
var (
	etcTimezone  = "/etc/timezone"
	etcLocaltime = "/etc/localtime"
)

// OSZoneName returns the OS timezone as an IANA name ("Europe/Berlin"), from
// /etc/timezone (Debian) or the /etc/localtime symlink. "" when neither says.
// The agent's system.info reads the same two files.
func OSZoneName() string {
	if data, err := os.ReadFile(etcTimezone); err == nil {
		if tz := strings.TrimSpace(string(data)); tz != "" {
			return tz
		}
	}
	if target, err := os.Readlink(etcLocaltime); err == nil {
		const prefix = "/usr/share/zoneinfo/"
		if i := strings.Index(target, prefix); i >= 0 {
			return target[i+len(prefix):]
		}
	}
	return ""
}

// LoadZone loads an IANA zone name. It refuses "", "Local" (the process zone,
// not a name the SPA can use), a path and anything the zone database does not
// have.
func LoadZone(name string) (*time.Location, bool) {
	if name == "" || name == "Local" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	return loc, true
}

// legacyZones maps the old zone names a browser lists to their current names
// (GH #2320). Intl.supportedValuesOf("timeZone") in Chrome, Safari and Node
// returns ICU's IDs, which keep spellings like Asia/Calcutta and Europe/Kiev.
// Debian 13 and Ubuntu 24.04 ship those only in the optional tzdata-legacy
// package: with no zone file, timedatectl, PHP and this binary (built with
// -trimpath, so no Go zone archive to fall back on) all refuse them. Each
// value is the alias's link target in tzdata.zi. The SPA keeps the same map
// in panel-ui/src/data/timezones.ts; a test keeps the two equal.
var legacyZones = map[string]string{
	"Africa/Asmera":        "Africa/Asmara",
	"America/Buenos_Aires": "America/Argentina/Buenos_Aires",
	"America/Catamarca":    "America/Argentina/Catamarca",
	"America/Cordoba":      "America/Argentina/Cordoba",
	"America/Godthab":      "America/Nuuk",
	"America/Indianapolis": "America/Indiana/Indianapolis",
	"America/Jujuy":        "America/Argentina/Jujuy",
	"America/Louisville":   "America/Kentucky/Louisville",
	"America/Mendoza":      "America/Argentina/Mendoza",
	"Asia/Calcutta":        "Asia/Kolkata",
	"Asia/Katmandu":        "Asia/Kathmandu",
	"Asia/Rangoon":         "Asia/Yangon",
	"Asia/Saigon":          "Asia/Ho_Chi_Minh",
	"Atlantic/Faeroe":      "Atlantic/Faroe",
	"Europe/Kiev":          "Europe/Kyiv",
	"Pacific/Enderbury":    "Pacific/Kanton",
	"Pacific/Ponape":       "Pacific/Pohnpei",
	"Pacific/Truk":         "Pacific/Chuuk",
}

// CanonicalZone returns the current name of a zone a browser lists by its old
// name, and any other name unchanged.
func CanonicalZone(name string) string {
	if current, ok := legacyZones[name]; ok {
		return current
	}
	return name
}

// Resolve picks the panel zone: the Server Settings value when it loads, else
// the OS zone, else UTC.
func Resolve(settingsZone string) (*time.Location, string) {
	if loc, ok := LoadZone(strings.TrimSpace(settingsZone)); ok {
		return loc, loc.String()
	}
	if loc, ok := LoadZone(OSZoneName()); ok {
		return loc, loc.String()
	}
	return time.UTC, "UTC"
}

type zone struct {
	loc  *time.Location
	name string
}

var current atomic.Pointer[zone]

// Set resolves the Server Settings timezone and makes it the panel zone.
func Set(settingsZone string) {
	loc, name := Resolve(settingsZone)
	current.Store(&zone{loc: loc, name: name})
}

// Location is the panel zone. UTC until Set runs.
func Location() *time.Location {
	if z := current.Load(); z != nil {
		return z.loc
	}
	return time.UTC
}

// Name is the panel zone's IANA name. "UTC" until Set runs.
func Name() string {
	if z := current.Load(); z != nil {
		return z.name
	}
	return "UTC"
}
