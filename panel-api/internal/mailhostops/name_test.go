package mailhostops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390: a requested mail hostname must not be the panel hostname, a name
// a hosted domain answers, a name under a tenant's zone (the tenant controls
// its DNS), or a tenant's web alias. The setter refuses such a request and
// the engine re-checks before it issues and before it applies, because a
// tenant can create a domain after the request is made.

type fakeDomains struct {
	byName map[string]*models.Domain
	errFor string
	err    error
}

func (f *fakeDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if name == f.errFor {
		return nil, f.err
	}
	if d, ok := f.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type fakeAliases struct {
	held map[string]bool
	err  error
}

func (f *fakeAliases) FindByHostname(_ context.Context, host string) (*models.WebDomainAlias, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.held[host] {
		return &models.WebDomainAlias{Hostname: host}, nil
	}
	return nil, repository.ErrNotFound
}

func nameDeps() (NameDeps, *fakeDomains, *fakeAliases) {
	doms := &fakeDomains{byName: map[string]*models.Domain{
		"panel.example.com": {Name: "panel.example.com", IsPanelPrimary: true},
		"tenant.net":        {Name: "tenant.net"},
	}}
	als := &fakeAliases{held: map[string]bool{"shop.alias.org": true}}
	return NameDeps{Domains: doms, Aliases: als}, doms, als
}

func TestCheckName(t *testing.T) {
	s := &models.ServerSettings{Hostname: "panel.example.com"}
	cases := []struct {
		desired string
		want    error
	}{
		{"mx.example.org", nil},
		{"mail.panel.example.com", nil},
		{"mailtest.panel.example.com", nil},
		{"panel.example.com", ErrNameIsPanelHostname},
		{"www.panel.example.com", ErrNameClaimedByDomain},
		{"autodiscover.panel.example.com", ErrNameClaimedByDomain},
		{"tenant.net", ErrNameClaimedByDomain},
		{"mail.tenant.net", ErrNameClaimedByDomain},
		{"mx.deep.tenant.net", ErrNameClaimedByDomain},
		{"shop.alias.org", ErrNameIsAlias},
	}
	for _, tc := range cases {
		t.Run(tc.desired, func(t *testing.T) {
			deps, _, _ := nameDeps()
			err := CheckName(context.Background(), deps, s, tc.desired)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("CheckName = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("CheckName = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckName_LookupErrorsFailClosed(t *testing.T) {
	s := &models.ServerSettings{Hostname: "panel.example.com"}
	boom := errors.New("db down")

	deps, doms, _ := nameDeps()
	doms.errFor, doms.err = "example.org", boom
	if err := CheckName(context.Background(), deps, s, "mx.example.org"); !errors.Is(err, boom) {
		t.Fatalf("an ancestor lookup error must refuse, got %v", err)
	}

	deps, _, als := nameDeps()
	als.err = boom
	if err := CheckName(context.Background(), deps, s, "mx.example.org"); !errors.Is(err, boom) {
		t.Fatalf("an alias lookup error must refuse, got %v", err)
	}

	if err := CheckName(context.Background(), NameDeps{}, s, "mx.example.org"); err == nil {
		t.Fatal("no domain lookup wired must refuse, not pass")
	}
}
