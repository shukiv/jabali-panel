package cronvalidate

import "testing"

func TestCronToSystemdCalendar(t *testing.T) {
	cases := map[string]string{
		"*/5 * * * *":   "*-*-* *:0/5:00",
		"0 0 * * *":     "*-*-* 0:0:00",
		"30 2 * * 1":    "Mon *-*-* 2:30:00",
		"0 */6 * * *":   "*-*-* 0/6:0:00",
		"15 10 1 * *":   "*-*-1 10:15:00",
		"0 9 * * 1-5":   "Mon..Fri *-*-* 9:0:00",
		"*/15 * * * *":  "*-*-* *:0/15:00",
		"0 0 * * 0":     "Sun *-*-* 0:0:00",
		"0 0 1,15 * *":  "*-*-1,15 0:0:00",
		"0 0 * * 7":     "Sun *-*-* 0:0:00",
	}
	for in, want := range cases {
		got, err := CronToSystemdCalendar(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CronToSystemdCalendar(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCronToSystemdCalendar_BadFieldCount(t *testing.T) {
	if _, err := CronToSystemdCalendar("*/5 * * *"); err == nil {
		t.Error("expected error for 4-field expr")
	}
}
