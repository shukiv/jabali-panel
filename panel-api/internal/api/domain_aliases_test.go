package api

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The fakes embed the repository interfaces so only the methods
// validateAliasHostname actually calls need bodies.

type aliasTestDomains struct {
	repository.DomainRepository
	names map[string]*models.Domain
}

func (f aliasTestDomains) FindByName(_ context.Context, n string) (*models.Domain, error) {
	if d, ok := f.names[strings.ToLower(n)]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type aliasTestAliases struct {
	repository.WebDomainAliasRepository
	taken map[string]bool
}

func (f aliasTestAliases) FindByHostname(_ context.Context, h string) (*models.WebDomainAlias, error) {
	if f.taken[strings.ToLower(h)] {
		return &models.WebDomainAlias{Hostname: strings.ToLower(h)}, nil
	}
	return nil, repository.ErrNotFound
}

type aliasTestSettings struct {
	repository.ServerSettingsRepository
	hostname string
}

func (f aliasTestSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: f.hostname, PublicIPv4: "203.0.113.9"}, nil
}

// GH #1625: validateAliasHostname is the security boundary — it must reject an
// alias that would hijack another tenant's apex or helper vhost (a duplicate
// nginx server_name silently lets the first server block win), the panel's own
// FQDN, the domain's own primary/www, an already-claimed alias, and any value
// that isn't a clean FQDN (config-injection). Everything else is accepted.
func TestValidateAliasHostname(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "example.com", UserID: "u1"}
	other := &models.Domain{ID: "d2", Name: "other.com", UserID: "u2"}
	h := &domainAliasHandler{cfg: DomainAliasHandlerConfig{
		Domains: aliasTestDomains{names: map[string]*models.Domain{
			"example.com": dom,
			"other.com":   other,
		}},
		Aliases:  aliasTestAliases{taken: map[string]bool{"taken.example.net": true}},
		Settings: aliasTestSettings{hostname: "panel.host.com"},
	}}

	cases := []struct {
		name string
		in   string
		code string // "" == accepted
	}{
		{"foreign subdomain accepted", "shop.brandy.io", ""},
		{"own subdomain accepted", "blog.example.com", ""},
		{"uppercase normalized + accepted", "Shop.Brandy.IO", ""},
		{"own primary rejected", "example.com", "alias_is_primary"},
		{"own www rejected", "www.example.com", "alias_is_www"},
		{"another domain apex rejected", "other.com", "alias_taken_by_domain"},
		{"another domain mail helper rejected", "mail.other.com", "alias_conflicts_helper"},
		{"another domain www helper rejected", "www.other.com", "alias_conflicts_helper"},
		{"another domain mta-sts helper rejected", "mta-sts.other.com", "alias_conflicts_helper"},
		{"panel FQDN rejected", "panel.host.com", "alias_reserved_panel"},
		{"already-claimed alias rejected", "taken.example.net", "alias_exists"},
		{"semicolon injection rejected", "evil.net; return 301 http://x", "invalid_hostname"},
		{"space rejected", "a b.net", "invalid_hostname"},
		{"newline rejected", "a.net\nb.net", "invalid_hostname"},
		{"single label rejected", "localhost", "invalid_hostname"},
		{"empty rejected", "", "invalid_hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, status, code, _ := h.validateAliasHostname(context.Background(), dom, tc.in)
			if tc.code == "" {
				if status != 0 {
					t.Fatalf("expected accept, got status=%d code=%q", status, code)
				}
				if host != strings.ToLower(strings.TrimSpace(tc.in)) {
					t.Errorf("normalized host = %q, want %q", host, strings.ToLower(strings.TrimSpace(tc.in)))
				}
				return
			}
			if code != tc.code {
				t.Fatalf("code = %q (status %d), want %q", code, status, tc.code)
			}
			if status == 0 {
				t.Errorf("rejection must carry a non-zero HTTP status")
			}
		})
	}
}

// GH #1625: a web-disabled domain (DNS-only / mail-only) has no vhost, so an
// alias would never render into a server_name — the row would be inert yet
// squat a globally-unique hostname. validateAliasHostname must refuse it.
func TestValidateAliasHostname_WebDisabled(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "example.com", UserID: "u1", WebDisabled: true}
	h := &domainAliasHandler{cfg: DomainAliasHandlerConfig{
		Domains: aliasTestDomains{names: map[string]*models.Domain{"example.com": dom}},
		Aliases: aliasTestAliases{},
	}}
	_, status, code, _ := h.validateAliasHostname(context.Background(), dom, "shop.brandy.io")
	if code != "domain_has_no_web" {
		t.Fatalf("code = %q (status %d), want domain_has_no_web", code, status)
	}
	if status != 400 {
		t.Errorf("status = %d, want 400", status)
	}
}

// GH #1625: aliasCollision is the REVERSE guard — domain-create/rename must
// reject a name whose apex OR any of its rendered server_names (www + the four
// mail helpers) is already claimed by another domain's alias. Without it, a
// domain created after a matching alias reopens the cross-tenant hijack hole
// (duplicate nginx server_name → first-loaded wins).
func TestAliasCollision(t *testing.T) {
	aliases := aliasTestAliases{taken: map[string]bool{
		"claimed.example.net":  true, // another domain's alias == this apex
		"www.wwwclaimed.com":   true, // == this domain's www server_name
		"mail.mailclaimed.com": true, // == this domain's mail helper server_name
	}}
	cases := []struct {
		name    string
		in      string
		wantHit string
		clash   bool
	}{
		{"apex collides", "claimed.example.net", "claimed.example.net", true},
		{"www helper collides", "wwwclaimed.com", "www.wwwclaimed.com", true},
		{"mail helper collides", "mailclaimed.com", "mail.mailclaimed.com", true},
		{"uppercase normalized then collides", "Claimed.Example.NET", "claimed.example.net", true},
		{"free name passes", "free.example.org", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, clash := aliasCollision(context.Background(), aliases, tc.in)
			if clash != tc.clash {
				t.Fatalf("clash = %v, want %v (hit %q)", clash, tc.clash, hit)
			}
			if clash && hit != tc.wantHit {
				t.Errorf("hit = %q, want %q", hit, tc.wantHit)
			}
		})
	}
	// A nil repo means the feature is unwired — fail-open, never a collision.
	if _, clash := aliasCollision(context.Background(), nil, "claimed.example.net"); clash {
		t.Fatal("nil repo must not report a collision")
	}
	// An empty name is not a lookup.
	if _, clash := aliasCollision(context.Background(), aliases, ""); clash {
		t.Fatal("empty name must not report a collision")
	}
}
