package dockerapp

import (
	"errors"
	"strings"
	"testing"
)

// GH #1956: Update never moves an install across a major its app can't take
// in place. These fixtures mirror the three catalog entries that use tracks.

func pin(n string) string { return "x:" + n + "@sha256:" + strings.Repeat(n[:1], 64) }

func odooEntry() Entry {
	return Entry{
		Name: "Odoo", Version: "20.0", ImageChannel: pin("2"), Track: "20",
		HeldTracks: []HeldTrack{
			{Track: "19", Version: "19.0", ImageChannel: pin("1")},
			{Track: "18", Version: "18.0", ImageChannel: pin("8")},
		},
	}
}

func nextcloudEntry() Entry {
	return Entry{
		Name: "Nextcloud", Version: "35.0.1", ImageChannel: pin("5"), Track: "35", UpdateFrom: []string{"34"},
		HeldTracks: []HeldTrack{{Track: "34", Version: "34.0.3", ImageChannel: pin("4"), UpdateFrom: []string{"33"}}},
	}
}

func giteaEntry() Entry {
	return Entry{
		Name: "Gitea", Version: "28.1.0", ImageChannel: pin("a"), Track: "28", UpdateFrom: []string{"1.27", "1.26"},
		HeldTracks: []HeldTrack{{Track: "1.27", Version: "1.27.3", ImageChannel: pin("b")}},
	}
}

func TestTargetFor(t *testing.T) {
	plain := Entry{Name: "n8n", Version: "2.42.2", ImageChannel: pin("c")}
	for _, tc := range []struct {
		name    string
		entry   Entry
		install string
		update  bool
		want    string // target version; "" = ErrNoUpdatePath
		notice  string // substring of Target.Notice, or of the error; "" = no notice
	}{
		{"an entry without a track updates every install", plain, "1.0.0", true, "2.42.2", ""},
		{"an entry without a track re-renders every install at its version", plain, "1.0.0", false, "2.42.2", ""},

		{"odoo 20 updates within 20", odooEntry(), "20.0", true, "20.0", ""},
		{"odoo 19 Update stays on 19 and says why", odooEntry(), "19.0", true, "19.0", "Odoo 20.0 is for new installs. This install stays on Odoo 19.0"},
		{"odoo 19 env edit stays on 19", odooEntry(), "19.0", false, "19.0", ""},
		{"odoo 18 stays on 18", odooEntry(), "18.0", true, "18.0", "stays on Odoo 18.0"},
		{"odoo 17 has no image", odooEntry(), "17.0", true, "", "Back it up and migrate it by hand."},
		{"odoo 17 env edit has no image", odooEntry(), "17.0", false, "", "Back it up and migrate it by hand."},
		{"an install with no recorded version has no image", odooEntry(), "", true, "", ""},

		{"nextcloud 35 updates within 35", nextcloudEntry(), "35.0.0", true, "35.0.1", ""},
		{"nextcloud 34 Update moves to 35", nextcloudEntry(), "34.0.2", true, "35.0.1", ""},
		{"nextcloud 34 env edit stays on 34", nextcloudEntry(), "34.0.2", false, "34.0.3", ""},
		{"nextcloud 33 Update steps to 34 first", nextcloudEntry(), "33.0.5", true, "34.0.3", "Run Update again afterwards to reach 35.0.1"},
		{"nextcloud 33 env edit has no image", nextcloudEntry(), "33.0.5", false, "", "Update it first: Update moves it to 34.0.3."},
		{"nextcloud 30 has no path", nextcloudEntry(), "30.0.0", true, "", ""},

		{"gitea 1.27 Update moves to 28", giteaEntry(), "1.27.3", true, "28.1.0", ""},
		{"gitea 1.27 env edit stays on 1.27", giteaEntry(), "1.27.3", false, "1.27.3", ""},
		{"gitea 1.26 Update moves to 28", giteaEntry(), "1.26.2", true, "28.1.0", ""},
		// No held 1.26 image: an edit renders the current one, as Update
		// would, and the label follows (the status quo before tracks).
		{"gitea 1.26 env edit renders 28", giteaEntry(), "1.26.2", false, "28.1.0", ""},
		{"gitea 1.22 has no path", giteaEntry(), "1.22.3", true, "", ""},
		{"track 1.2 is not track 1.27", giteaEntry(), "1.2.0", true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.entry.TargetFor(tc.install, tc.update)
			if tc.want == "" {
				if !errors.Is(err, ErrNoUpdatePath) {
					t.Fatalf("got %+v, %v; want ErrNoUpdatePath", got, err)
				}
				if !strings.Contains(err.Error(), tc.entry.Name+" "+tc.install+",") || !strings.Contains(err.Error(), tc.notice) {
					t.Fatalf("error %q should name the recorded version and contain %q", err, tc.notice)
				}
				return
			}
			if err != nil {
				t.Fatalf("err %v", err)
			}
			if got.Version != tc.want {
				t.Fatalf("version %q, want %q", got.Version, tc.want)
			}
			wantImage := tc.entry.ImageChannel
			for _, h := range tc.entry.HeldTracks {
				if h.Version == tc.want {
					wantImage = h.ImageChannel
				}
			}
			if got.ImageChannel != wantImage {
				t.Fatalf("image %q, want %q", got.ImageChannel, wantImage)
			}
			if (tc.notice == "") != (got.Notice == "") || !strings.Contains(got.Notice, tc.notice) {
				t.Fatalf("notice %q, want one containing %q", got.Notice, tc.notice)
			}
		})
	}
}

func TestOnTrack(t *testing.T) {
	for _, tc := range []struct {
		v, t string
		want bool
	}{
		{"19.0", "19", true},
		{"19", "19", true},
		{"1.27.3", "1.27", true},
		{"1.27.3", "1.2", false},
		{"190.0", "19", false},
		{"19.0", "", false},
	} {
		if got := onTrack(tc.v, tc.t); got != tc.want {
			t.Errorf("onTrack(%q, %q) = %v, want %v", tc.v, tc.t, got, tc.want)
		}
	}
}

func TestEntry_Validate_Tracks(t *testing.T) {
	base := func() Entry {
		e := nextcloudEntry()
		e.Slug, e.Description = "nextcloud", "x"
		e.Volumes = []Volume{{Name: "data", ContainerPath: "/data"}}
		e.Ports = []PortSpec{{Name: "http", ContainerPort: 80, Protocol: "tcp", DefaultBind: "loopback"}}
		return e
	}
	if err := base().validate(); err != nil {
		t.Fatalf("a valid tracked entry: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Entry)
		want   string
	}{
		{"version off its track", func(e *Entry) { e.Version = "34.0.3" }, `version "34.0.3" is not on track "35"`},
		{"update_from without a track", func(e *Entry) { e.Track, e.HeldTracks = "", nil }, "need a track"},
		{"held tracks without a track", func(e *Entry) { e.Track, e.UpdateFrom = "", nil }, "need a track"},
		{"update_from naming its own track", func(e *Entry) { e.UpdateFrom = []string{"35"} }, "update_from[0]"},
		{"an empty update_from", func(e *Entry) { e.UpdateFrom = []string{""} }, "update_from[0]"},
		{"a held version off its track", func(e *Entry) { e.HeldTracks[0].Version = "33.0.5" }, `held_tracks[0].version "33.0.5"`},
		{"a held image not digest-pinned", func(e *Entry) { e.HeldTracks[0].ImageChannel = "nextcloud:34-apache" }, "held_tracks[0].image_channel"},
		{"a held track equal to the current one", func(e *Entry) { e.HeldTracks[0].Track, e.HeldTracks[0].Version = "35", "35.0.0" }, "held_tracks[0].track"},
		{"a held track listed twice", func(e *Entry) { e.HeldTracks = append(e.HeldTracks, e.HeldTracks[0]) }, "held_tracks[1].track"},
		{"a held track without a name", func(e *Entry) { e.HeldTracks[0].Track = "" }, "held_tracks[0].track"},
		{"a held update_from naming its own track", func(e *Entry) { e.HeldTracks[0].UpdateFrom = []string{"34"} }, "held_tracks[0].update_from[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base()
			e.HeldTracks = append([]HeldTrack(nil), e.HeldTracks...)
			tc.mutate(&e)
			err := e.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// Every version label the shipped catalog has given an install of these apps
// (git log of their app.yaml) still has somewhere to go: Update moves it on
// or keeps it on its own major, never across one the app can't take.
func TestCatalogTracks_ShippedLabels(t *testing.T) {
	cat, errs := LoadDir(catalogRoot(t))
	if len(errs) > 0 {
		t.Fatalf("catalog load: %v", errs)
	}
	for _, tc := range []struct {
		slug, label, want string
	}{
		{"odoo", "18.0", "18.0"},
		{"odoo", "19.0", "19.0"},
		{"odoo", "20.0", "20.0"},
		{"nextcloud", "33.0.5", "34.0.4"},
		{"nextcloud", "34.0.2", "35.0.1"},
		{"nextcloud", "34.0.3", "35.0.1"},
		{"gitea", "1.26.2", "28.1.0"},
		{"gitea", "1.27.1", "28.1.0"},
		{"gitea", "1.27.3", "28.1.0"},
	} {
		e, ok := cat.Get(tc.slug)
		if !ok {
			t.Fatalf("%s not in the catalog", tc.slug)
		}
		got, err := e.TargetFor(tc.label, true)
		if err != nil || got.Version != tc.want {
			t.Errorf("%s %s: Update target %q, %v; want %s", tc.slug, tc.label, got.Version, err, tc.want)
		}
	}
}
