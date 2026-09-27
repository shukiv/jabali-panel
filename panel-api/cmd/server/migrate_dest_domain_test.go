package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The derived name is canonical (lowercase) before "www." is dropped, so a
// siteurl's letter case can neither keep the www label nor store a
// mixed-case name the lookups miss.
func TestDeriveDomainFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://shop.example.com/":      "shop.example.com",
		"https://WWW.Shop.Example.COM/x": "shop.example.com",
		"http://www.example.com:8080":    "example.com",
		" https://Example.com ":          "example.com",
		"not a url":                      "",
		"":                               "",
	} {
		if got := deriveDomainFromURL(in); got != want {
			t.Errorf("deriveDomainFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

type migrationPanelSettings struct{ hostname string }

func (s migrationPanelSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: s.hostname}, nil
}

// migrationDestDomainInput is a tenant create: the rules the tenant REST door
// runs refuse a siteurl pointing into another tenant's zone, at the panel's
// own names, at a non-FQDN, or past the owner's package quota. A name that
// passes is stored with mail off, as the auto-create always did.
func TestMigrationDestDomainInput_TenantRules(t *testing.T) {
	uname := "alice"
	pkg := "pkg-1"
	alice := &models.User{ID: "u-alice", Username: &uname}
	full := &models.User{ID: "u-full", Username: &uname, PackageID: &pkg}
	owners := cliCreateOwners{"u-alice": alice, "u-full": full}

	refused := []struct {
		name  string
		owner string
		dom   string
		deps  func(*domainops.CreateDeps)
		want  error
	}{
		{name: "another tenant's zone", owner: "u-alice", dom: "shop.example.com",
			deps: func(d *domainops.CreateDeps) {
				d.Domains = &cliSuffixStore{parent: &models.Domain{Name: "example.com", UserID: "u-other"}}
			},
			want: domainops.ErrDomainConflictsTenant},
		{name: "the panel hostname", owner: "u-alice", dom: "panel.example.net",
			deps: func(d *domainops.CreateDeps) { d.Settings = migrationPanelSettings{hostname: "panel.example.net"} },
			want: domainops.ErrDomainConflictsMailHostname},
		{name: "the panel mail hostname", owner: "u-alice", dom: "mail.panel.example.net",
			deps: func(d *domainops.CreateDeps) { d.Settings = migrationPanelSettings{hostname: "panel.example.net"} },
			want: domainops.ErrDomainConflictsMailHostname},
		{name: "package quota reached", owner: "u-full", dom: "shop.example.com",
			deps: func(d *domainops.CreateDeps) {
				d.Domains = &cliCreateStore{count: 1}
				d.Packages = quotaPackages{max: 1}
			},
			want: domainops.ErrDomainQuotaExceeded},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			deps := domainops.CreateDeps{Domains: &cliCreateStore{}, Users: owners}
			tc.deps(&deps)
			_, err := domainops.Create(context.Background(), deps, domainops.CreateHooks{}, migrationDestDomainInput(tc.owner, tc.dom))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	for _, bad := range []string{"203.0.113.5", "localhost", "shop_1.example.com"} {
		t.Run("not a domain name: "+bad, func(t *testing.T) {
			store := &cliCreateStore{}
			_, err := domainops.Create(context.Background(), domainops.CreateDeps{Domains: store, Users: owners},
				domainops.CreateHooks{}, migrationDestDomainInput("u-alice", bad))
			if err == nil || len(store.created) != 0 {
				t.Fatalf("%q: want a refusal and no row, got err=%v rows=%d", bad, err, len(store.created))
			}
		})
	}

	t.Run("a free name is stored for the owner with mail off", func(t *testing.T) {
		store := &cliCreateStore{}
		res, err := domainops.Create(context.Background(), domainops.CreateDeps{Domains: store, Users: owners},
			domainops.CreateHooks{}, migrationDestDomainInput("u-alice", "shop.example.com"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		d := res.Domain
		if d.UserID != "u-alice" || d.Name != "shop.example.com" ||
			d.DocRoot != "/home/alice/domains/shop.example.com/public_html" ||
			d.SSLMode != models.SSLModeLE || d.MailProvider != models.MailProviderNone || d.EmailEnabled {
			t.Fatalf("unexpected stored domain: %+v", d)
		}
	})
}

// A tenant's WordPress migration with no destination domain auto-creates one
// named after the source site's siteurl, which the tenant controls. That create
// must run the rules the REST create door runs for a tenant (name validation,
// the alias, cross-tenant and panel-name guards, the package quota), so it is
// domainops.Create with the job owner as a tenant actor, never a raw insert.
func TestResolveOrCreateDestDomain_UsesTheCreateEntrypoint(t *testing.T) {
	src := stripLineComments(readGoSource(t, "migrate_pull_cmd.go"))
	start := strings.Index(src, "func resolveOrCreateDestDomain(")
	if start < 0 {
		t.Fatal("expected resolveOrCreateDestDomain in migrate_pull_cmd.go")
	}
	end := strings.Index(src[start:], "\n}\n")
	fn := src[start : start+end]

	for _, re := range []string{
		`domainops\.Create\(ctx, deps, domainops\.CreateHooks\{\}, migrationDestDomainInput\(uid, dom\)\)`,
		`Domains:\s+domainRepoFromDB\(\),`,
		`Users:\s+repository\.NewUserRepository\(sharedDB\),`,
		`Aliases:\s+repository\.NewWebDomainAliasRepository\(sharedDB\),`,
		`Packages:\s+packageRepoFromDB\(\),`,
		`Settings:\s+serverSettingsRepoFromDB\(\),`,
		// A failed lookup stops; it never falls through to the create.
		`case err != nil && !errors\.Is\(err, repository\.ErrNotFound\):\s*fmt\.Printf\([^\n]*\)\s*return\n`,
	} {
		if !regexp.MustCompile(re).MatchString(fn) {
			t.Errorf("resolveOrCreateDestDomain must contain %s", re)
		}
	}
	for _, banned := range []string{"models.Domain{", "domains.Create(", ".Create(ctx, row)"} {
		if strings.Contains(fn, banned) {
			t.Errorf("resolveOrCreateDestDomain must not insert a domain row itself (%q); domainops.Create owns it", banned)
		}
	}
}
