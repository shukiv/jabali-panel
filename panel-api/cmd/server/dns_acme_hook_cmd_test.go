package main

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeZoneByNameRepo struct {
	repository.DNSZoneRepository
	zones map[string]*models.DNSZone
}

func (f *fakeZoneByNameRepo) FindByName(_ context.Context, name string) (*models.DNSZone, error) {
	if z, ok := f.zones[name]; ok {
		return z, nil
	}
	return nil, repository.ErrNotFound
}

func TestFindZoneForName_WalksParentChain(t *testing.T) {
	repo := &fakeZoneByNameRepo{zones: map[string]*models.DNSZone{
		"example.com": {ID: "Z1", Name: "example.com"},
		"host.tld":    {ID: "Z2", Name: "host.tld"},
	}}
	ctx := context.Background()

	cases := []struct {
		name   string
		wantID string
	}{
		// certbot passes CERTBOT_DOMAIN without the "*." for wildcards, so
		// a *.example.com challenge arrives as "example.com".
		{"example.com", "Z1"},
		{"_acme-challenge.example.com", "Z1"},
		{"preview.host.tld", "Z2"},
		{"deep.sub.preview.host.tld", "Z2"},
	}
	for _, tc := range cases {
		z, err := findZoneForName(ctx, repo, tc.name)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if z.ID != tc.wantID {
			t.Errorf("%s: matched zone %s, want %s", tc.name, z.ID, tc.wantID)
		}
	}
}

func TestFindZoneForName_RefusesForeignZone(t *testing.T) {
	repo := &fakeZoneByNameRepo{zones: map[string]*models.DNSZone{}}
	if _, err := findZoneForName(context.Background(), repo, "elsewhere.net"); err == nil {
		t.Fatal("expected an error for a name with no local zone")
	}
}

func TestAcmeChallengeName(t *testing.T) {
	if got := acmeChallengeName("preview.host.tld"); got != "_acme-challenge.preview.host.tld" {
		t.Errorf("got %q", got)
	}
}
