package api

import "testing"

// GH #1332: the per-domain error_reporting bitmask and date.timezone are the two
// new free-ish inputs; both feed a fastcgi_param PHP_VALUE line, so their
// validators are the injection/typo boundary.

func TestValidateErrorReporting(t *testing.T) {
	cases := []struct {
		name string
		v    int
		ok   bool
	}{
		{"zero reports nothing", 0, true},
		{"production bitmask", 22527, true},
		{"E_ALL", 32767, true},
		{"above E_ALL", 32768, false},
		{"negative", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateErrorReporting(tc.v)
			if tc.ok && err != nil {
				t.Errorf("validateErrorReporting(%d) = %v, want nil", tc.v, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("validateErrorReporting(%d) = nil, want error", tc.v)
			}
		})
	}
}

func TestValidateTimezone(t *testing.T) {
	cases := []struct {
		name string
		v    string
		ok   bool
	}{
		{"utc", "UTC", true},
		{"area/city", "Europe/Berlin", true},
		{"etc offset", "Etc/GMT+5", true},
		{"empty", "", false},
		{"unknown zone", "Mars/Olympus", false},
		{"newline injection", "UTC\ndisplay_errors=On", false},
		{"quote injection", "UTC\"", false},
		{"too long", "A/" + string(make([]byte, 64)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTimezone(tc.v)
			if tc.ok && err != nil {
				t.Errorf("validateTimezone(%q) = %v, want nil", tc.v, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("validateTimezone(%q) = nil, want error", tc.v)
			}
		})
	}
}
