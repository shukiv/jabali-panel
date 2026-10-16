package backup

import (
	"testing"
	"time"
	_ "time/tzdata" // the zones below load on a host without tzdata too

	"git.jabali-panel.com/shukivaknin/jabali2/internal/paneltime"
)

// setPanelZone makes name the panel clock for one test.
func setPanelZone(t *testing.T, name string) {
	t.Helper()
	paneltime.Set(name)
	if paneltime.Name() != name {
		t.Fatalf("panel zone = %q, want %q", paneltime.Name(), name)
	}
	t.Cleanup(func() { paneltime.Set("UTC") })
}

// GH #2164: "0 3 * * *" is 03:00 on the server's clock, the time the panel
// shows, not 03:00 UTC. The answer stays a UTC instant.
func TestNextFire_ReadsTheCronOnThePanelClock(t *testing.T) {
	setPanelZone(t, "Europe/Berlin")
	for _, tc := range []struct {
		name string
		from time.Time
		want time.Time
	}{
		{"winter, UTC+1", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), time.Date(2026, 1, 16, 2, 0, 0, 0, time.UTC)},
		{"summer, UTC+2", time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC)},
	} {
		got, err := NextFire("0 3 * * *", tc.from)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s: NextFire = %s, want %s (03:00 Berlin)", tc.name, got, tc.want)
		}
		if got.Location() != time.UTC {
			t.Errorf("%s: NextFire location = %s, want UTC", tc.name, got.Location())
		}
	}
}

func TestPreviewFires_PanelClockAcrossDST(t *testing.T) {
	setPanelZone(t, "Europe/Berlin")
	// Berlin moves to summer time on 2026-03-29.
	got, err := PreviewFires("0 3 * * *", time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{
		time.Date(2026, 3, 28, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 30, 1, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("PreviewFires = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) || got[i].Location() != time.UTC {
			t.Errorf("fire %d = %s, want %s UTC", i, got[i], want[i])
		}
	}
}

func TestNextFire_UTCPanelClockUnchanged(t *testing.T) {
	setPanelZone(t, "UTC")
	got, err := NextFire("0 3 * * *", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 16, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextFire = %s, want %s", got, want)
	}
}

// GH #2164: the admin's tenant backup window (02:00–05:00) is server time too.
func TestTenantWindow_ReadsThePanelClock(t *testing.T) {
	setPanelZone(t, "Europe/Berlin")
	w := ParseWindow("02:00", "05:00") // no Loc: the panel clock
	in := time.Date(2026, 1, 15, 1, 30, 0, 0, time.UTC)    // 02:30 Berlin
	out := time.Date(2026, 1, 15, 4, 30, 0, 0, time.UTC)   // 05:30 Berlin
	utcIn := time.Date(2026, 1, 15, 3, 30, 0, 0, time.UTC) // 04:30 Berlin, 03:30 UTC
	if !w.Contains(in) {
		t.Errorf("02:30 Berlin should be inside 02:00–05:00")
	}
	if w.Contains(out) {
		t.Errorf("05:30 Berlin should be outside 02:00–05:00")
	}
	if !w.Contains(utcIn) {
		t.Errorf("04:30 Berlin should be inside 02:00–05:00")
	}
	if w.Contains(time.Date(2026, 1, 15, 0, 30, 0, 0, time.UTC)) { // 01:30 Berlin, 00:30 UTC
		t.Errorf("01:30 Berlin should be outside 02:00–05:00")
	}
	got := w.NextOpen(time.Date(2026, 1, 15, 5, 0, 0, 0, time.UTC)) // 06:00 Berlin
	if want := time.Date(2026, 1, 16, 1, 0, 0, 0, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("NextOpen = %s, want %s (02:00 Berlin next day)", got, want)
	}
}

// The opening keeps its wall-clock minute across a DST change: 04:00 Berlin is
// 03:00 UTC in winter and 02:00 UTC on the first summer day, not 24h later.
func TestTenantWindow_NextOpenAcrossDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := TenantWindow{StartMin: 4 * 60, EndMin: 6 * 60, Loc: berlin}
	got := w.NextOpen(time.Date(2026, 3, 28, 6, 0, 0, 0, time.UTC)) // 07:00 CET, day before the change
	if want := time.Date(2026, 3, 29, 2, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextOpen = %s, want %s (04:00 CEST)", got, want)
	}
}
