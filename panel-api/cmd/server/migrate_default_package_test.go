package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type settingsRow struct {
	row *models.ServerSettings
	err error
}

func (s settingsRow) Get(context.Context) (*models.ServerSettings, error) { return s.row, s.err }

func strPtr(s string) *string { return &s }

// A migration that creates its user with no target package gives it the
// server's default package; a named target still wins.
func TestMigrationPackageID(t *testing.T) {
	withDefault := settingsRow{row: &models.ServerSettings{DefaultPackageID: strPtr("01DEFAULTPKG00000000000000")}}
	for _, c := range []struct {
		name        string
		target      string
		settings    settingsRow
		want        string
		fromDefault bool
		wantErr     bool
	}{
		{"target wins", "01TARGETPKG000000000000000", withDefault, "01TARGETPKG000000000000000", false, false},
		{"default when no target", "", withDefault, "01DEFAULTPKG00000000000000", true, false},
		{"no default set", "", settingsRow{row: &models.ServerSettings{}}, "", false, false},
		{"empty default", "", settingsRow{row: &models.ServerSettings{DefaultPackageID: strPtr("")}}, "", false, false},
		{"no settings row", "", settingsRow{err: repository.ErrNotFound}, "", false, false},
		{"settings unreadable", "", settingsRow{err: errors.New("db down")}, "", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, fromDefault, err := migrationPackageID(context.Background(), c.target, c.settings)
			if (err != nil) != c.wantErr || got != c.want || fromDefault != c.fromDefault {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, err=%v)", got, fromDefault, err, c.want, c.fromDefault, c.wantErr)
			}
		})
	}
}

func TestSettingsKeyDefaultPackage(t *testing.T) {
	orig := lookupSettingsPackage
	t.Cleanup(func() { lookupSettingsPackage = orig })
	lookupSettingsPackage = func(_ context.Context, v string) (*models.HostingPackage, error) {
		if v == "Tier 3" || v == "01TIER3PKG0000000000000000" {
			return &models.HostingPackage{ID: "01TIER3PKG0000000000000000", Name: "Tier 3"}, nil
		}
		return nil, fmt.Errorf("package %q not found", v)
	}
	def := settableKeys["default_package"]
	s := &models.ServerSettings{}
	if err := def.apply(s, " Tier 3 "); err != nil || s.DefaultPackageID == nil || *s.DefaultPackageID != "01TIER3PKG0000000000000000" {
		t.Fatalf("by name: err=%v id=%v", err, s.DefaultPackageID)
	}
	if err := def.apply(s, "Nope"); err == nil || *s.DefaultPackageID != "01TIER3PKG0000000000000000" {
		t.Fatalf("unknown package must fail and keep the value: err=%v id=%v", err, s.DefaultPackageID)
	}
	if err := def.apply(s, ""); err != nil || s.DefaultPackageID != nil {
		t.Fatalf("empty must clear: err=%v id=%v", err, s.DefaultPackageID)
	}
}
