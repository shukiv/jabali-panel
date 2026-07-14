package plesk

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestParsePleskList(t *testing.T) {
	// `plesk bin subscription --list` prints one subscription name per
	// line; blanks and banner/comment lines are ignored.
	out := "example.com\n\nshop.example.net\n# banner line\n  spaced.org  \n"
	got := parsePleskList(out)
	want := []string{"example.com", "shop.example.net", "spaced.org"}
	if len(got) != len(want) {
		t.Fatalf("parsed %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i].ID != name || got[i].Login != name || got[i].Domain != name {
			t.Errorf("row %d = %+v, want ID/Login/Domain = %q", i, got[i], name)
		}
	}
}

func TestParsePleskList_Empty(t *testing.T) {
	if got := parsePleskList("\n  \n# only comments\n"); len(got) != 0 {
		t.Errorf("expected no rows, got %+v", got)
	}
}

func TestNew_Defaults(t *testing.T) {
	d := New()
	if d.Port != 22 {
		t.Errorf("default port = %d, want 22", d.Port)
	}
	if d.CommandTimeout == 0 {
		t.Error("command timeout must be non-zero")
	}
	d.SetAllowPrivate(true)
	if !d.AllowPrivate {
		t.Error("SetAllowPrivate(true) did not stick")
	}
}

// TestRegistered proves init() wired the plesk kind into the shared
// registry so the admin source-picker + import dispatch can find it.
func TestRegistered(t *testing.T) {
	d, err := migrate.Get(models.MigrationSourcePlesk)
	if err != nil {
		t.Fatalf("migrate.Get(%q) failed: %v", models.MigrationSourcePlesk, err)
	}
	if d == nil {
		t.Fatal("registered plesk discoverer is nil")
	}
	if _, ok := d.(migrate.AllowPrivateSetter); !ok {
		t.Error("plesk discoverer must implement AllowPrivateSetter")
	}
}
