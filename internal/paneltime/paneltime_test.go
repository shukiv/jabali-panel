package paneltime

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
	_ "time/tzdata" // the zones below load on a host without tzdata too
)

// osZoneFiles points the OS zone lookup at files in a temp dir for one test.
// timezone "" leaves /etc/timezone absent; link "" leaves /etc/localtime absent.
func osZoneFiles(t *testing.T, timezone, link string) {
	t.Helper()
	dir := t.TempDir()
	tzPath := filepath.Join(dir, "timezone")
	ltPath := filepath.Join(dir, "localtime")
	if timezone != "" {
		if err := os.WriteFile(tzPath, []byte(timezone+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if link != "" {
		if err := os.Symlink(link, ltPath); err != nil {
			t.Fatal(err)
		}
	}
	oldTZ, oldLT := etcTimezone, etcLocaltime
	etcTimezone, etcLocaltime = tzPath, ltPath
	t.Cleanup(func() { etcTimezone, etcLocaltime = oldTZ, oldLT })
}

func TestResolve_SettingsThenOSThenUTC(t *testing.T) {
	cases := []struct {
		name, settings, osFile, osLink, want string
	}{
		{"settings wins", "Europe/Berlin", "Asia/Jerusalem", "", "Europe/Berlin"},
		{"settings trimmed", "  Europe/Berlin ", "", "", "Europe/Berlin"},
		{"empty settings: /etc/timezone", "", "Asia/Jerusalem", "", "Asia/Jerusalem"},
		{"empty settings: /etc/localtime link", "", "", "/usr/share/zoneinfo/America/New_York", "America/New_York"},
		{"unknown settings zone falls to the OS", "Mars/Olympus", "Asia/Jerusalem", "", "Asia/Jerusalem"},
		{"nothing usable: UTC", "Mars/Olympus", "Not/AZone", "", "UTC"},
		{"nothing at all: UTC", "", "", "", "UTC"},
		{"Local is not a name", "Local", "", "", "UTC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			osZoneFiles(t, tc.osFile, tc.osLink)
			loc, name := Resolve(tc.settings)
			if name != tc.want || loc.String() != tc.want {
				t.Errorf("Resolve(%q) = %s / %q, want %q", tc.settings, loc, name, tc.want)
			}
		})
	}
}

func TestLoadZone_RefusesNonNames(t *testing.T) {
	for _, bad := range []string{"", "Local", "/etc/passwd", "../../etc/passwd", "Europe/../../etc/passwd", "Not/AZone"} {
		if _, ok := LoadZone(bad); ok {
			t.Errorf("LoadZone(%q) accepted", bad)
		}
	}
	for _, good := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires"} {
		if _, ok := LoadZone(good); !ok {
			t.Errorf("LoadZone(%q) refused", good)
		}
	}
}

func TestSet_LocationAndName(t *testing.T) {
	osZoneFiles(t, "", "")
	t.Cleanup(func() { Set("UTC") })
	Set("Asia/Tokyo")
	if Name() != "Asia/Tokyo" || Location().String() != "Asia/Tokyo" {
		t.Fatalf("after Set: %q / %s", Name(), Location())
	}
	if got := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).In(Location()).Hour(); got != 9 {
		t.Errorf("00:00Z in the panel zone = %02d:00, want 09:00", got)
	}
	Set("bogus")
	if Name() != "UTC" || Location() != time.UTC {
		t.Errorf("Set(bogus) with no OS zone: %q / %s, want UTC", Name(), Location())
	}
}

func TestEffectiveDateFormat(t *testing.T) {
	if DefaultDateFormat != "DD/MM/YY HH:mm" {
		t.Fatalf("default = %q, want DD/MM/YY HH:mm (GH #2164)", DefaultDateFormat)
	}
	if !ValidDateFormat(DefaultDateFormat) {
		t.Fatal("the default is not in DateFormats")
	}
	for _, f := range DateFormats {
		if got := EffectiveDateFormat(f); got != f {
			t.Errorf("EffectiveDateFormat(%q) = %q", f, got)
		}
	}
	for _, bad := range []string{"", "YYYY", "DD/MM/YY HH:mm<script>", "dd/mm/yy"} {
		if ValidDateFormat(bad) {
			t.Errorf("ValidDateFormat(%q) = true", bad)
		}
		if got := EffectiveDateFormat(bad); got != DefaultDateFormat {
			t.Errorf("EffectiveDateFormat(%q) = %q, want the default", bad, got)
		}
	}
}

// The SPA renders the formats the API hands it from its own preset table.
// Both sides must list the same formats: a Go-only format would reach the SPA
// with no seconds or date-only form, and a TS-only one could never be saved.
func TestDateFormats_MatchTheSPAPresets(t *testing.T) {
	const tsPath = "../../panel-ui/src/utils/dateFormats.ts"
	src, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("read %s: %v", tsPath, err)
	}
	re := regexp.MustCompile(`dateTime:\s*"([^"]+)"`)
	var ts []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		ts = append(ts, m[1])
	}
	if len(ts) != len(DateFormats) {
		t.Fatalf("SPA presets %v, Go formats %v: lengths differ", ts, DateFormats)
	}
	for i := range DateFormats {
		if ts[i] != DateFormats[i] {
			t.Errorf("preset %d: SPA %q, Go %q", i, ts[i], DateFormats[i])
		}
	}
}
