package mailhostops

import (
	"context"
	"errors"
	"strings"
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
	subErr error
}

func (f *fakeDomains) FindStrictSubdomains(_ context.Context, name string) ([]models.Domain, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	var out []models.Domain
	for n, d := range f.byName {
		if strings.HasSuffix(n, "."+name) {
			out = append(out, *d)
		}
	}
	return out, nil
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
	held   map[string]bool
	err    error
	subErr error
}

func (f *fakeAliases) FindStrictSubdomainHostnames(_ context.Context, name string) ([]string, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	var out []string
	for h := range f.held {
		if strings.HasSuffix(h, "."+name) {
			out = append(out, h)
		}
	}
	return out, nil
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
		"panel.example.com":    {Name: "panel.example.com", IsPanelPrimary: true},
		"tenant.net":           {Name: "tenant.net"},
		"login.mail.other.org": {Name: "login.mail.other.org"},
	}}
	als := &fakeAliases{held: map[string]bool{"shop.alias.org": true, "x.mail.alias2.org": true}}
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
		{"mail.other.org", ErrNameClaimedByDomain},
		{"other.org", ErrNameClaimedByDomain},
		{"example.com", nil},
		{"shop.alias.org", ErrNameIsAlias},
		{"mail.alias2.org", ErrNameHasAliasUnder},
		{"alias.org", ErrNameHasAliasUnder},
		{"mx.alias2.org", nil},
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

	deps, doms, _ = nameDeps()
	doms.subErr = boom
	if err := CheckName(context.Background(), deps, s, "mx.example.org"); !errors.Is(err, boom) {
		t.Fatalf("a lookup error for names under it must refuse, got %v", err)
	}

	deps, _, als := nameDeps()
	als.err = boom
	if err := CheckName(context.Background(), deps, s, "mx.example.org"); !errors.Is(err, boom) {
		t.Fatalf("an alias lookup error must refuse, got %v", err)
	}

	deps, _, als = nameDeps()
	als.subErr = boom
	if err := CheckName(context.Background(), deps, s, "mx.example.org"); !errors.Is(err, boom) {
		t.Fatalf("a lookup error for aliases under it must refuse, got %v", err)
	}

	if err := CheckName(context.Background(), NameDeps{}, s, "mx.example.org"); err == nil {
		t.Fatal("no domain lookup wired must refuse, not pass")
	}
}
