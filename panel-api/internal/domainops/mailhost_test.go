package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390: the panel's custom mail hostname is served by the panel-primary
// mail vhost and carries the panel's mail certificate. A tenant domain must
// never answer it (its apex and helper vhosts) or control its DNS (an
// ancestor zone could be repointed, or used for a DNS-01 certificate
// elsewhere). The panel-primary domain is admin-owned; it only must not
// hand the name to a vhost other than its mail one.

func TestMailHostnameConflict(t *testing.T) {
	cases := []struct {
		domain, host string
		primary      bool
		want         bool
	}{
		// Tenant domains: the name itself, a parent zone of it, or a name
		// under it.
		{"mx.example.net", "mx.example.net", false, true},
		{"example.net", "mx.example.net", false, true},
		{"example.net", "mail.example.net", false, true},
		{"example.net", "a.b.mx.example.net", false, true},
		{"EXAMPLE.net", "mx.example.NET", false, true},
		{"sub.mx.example.net", "mx.example.net", false, true},
		{"a.b.MX.example.net", "mx.example.net", false, true},
		{"other.net", "mx.example.net", false, false},
		{"xample.net", "mx.example.net", false, false},
		{"xmx.example.net", "mx.example.net", false, false},
		{"mx.example.net.evil.com", "mx.example.net", false, false},
		{"shop.example.net", "mx.example.net", false, false},
		// The panel-primary domain.
		{"panel.example.com", "panel.example.com", true, true},
		{"panel.example.com", "www.panel.example.com", true, true},
		{"panel.example.com", "autoconfig.panel.example.com", true, true},
		{"panel.example.com", "autodiscover.panel.example.com", true, true},
		{"panel.example.com", "mta-sts.panel.example.com", true, true},
		{"panel.example.com", "mail.panel.example.com", true, false},
		{"panel.example.com", "mailtest.panel.example.com", true, false},
		{"panel.example.com", "mx.example.com", true, false},
		// The admin-owned panel-primary domain may sit under the name.
		{"mx.example.com", "example.com", true, false},
		// Empty input never conflicts.
		{"", "mx.example.net", false, false},
		{"example.net", "", false, false},
	}
	for _, tc := range cases {
		if got := MailHostnameConflict(tc.domain, tc.host, tc.primary); got != tc.want {
			t.Errorf("MailHostnameConflict(%q, %q, primary=%v) = %v, want %v", tc.domain, tc.host, tc.primary, got, tc.want)
		}
	}
}

type fakeMailSettings struct {
	s   *models.ServerSettings
	err error
}

func (f *fakeMailSettings) Get(context.Context) (*models.ServerSettings, error) { return f.s, f.err }

func TestMailHostnameCollision(t *testing.T) {
	applied := "mx.example.net"
	withApplied := &fakeMailSettings{s: &models.ServerSettings{Hostname: "panel.example.com", MailHostname: &applied}}
	derived := &fakeMailSettings{s: &models.ServerSettings{Hostname: "panel.example.com"}}

	for _, tc := range []struct {
		name     string
		settings MailSettingsReader
		domain   string
		want     bool
	}{
		{"claims the applied name", withApplied, "mx.example.net", true},
		{"claims its zone", withApplied, "example.net", true},
		{"claims a name under it", withApplied, "login.mx.example.net", true},
		{"unrelated", withApplied, "shop.example.org", false},
		{"no custom name applied", derived, "example.net", false},
		{"unwired", nil, "mx.example.net", false},
		{"no settings row", &fakeMailSettings{err: repository.ErrNotFound}, "mx.example.net", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MailHostnameCollision(context.Background(), tc.settings, tc.domain)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("collision = %v, want %v", got, tc.want)
			}
		})
	}
}

// The panel hostname and the derived mail.<hostname> are the panel's own
// names whether or not a panel-primary domain row exists (install.sh creates
// that row only with the dns module). The derived name stays reserved after a
// switchover, because the old name is served alongside the new one. Only the
// exact names are reserved: a parent zone of the panel hostname may be an
// admin's own site hosted as a tenant, and names under it are left to the
// cross-tenant guard.
func TestMailHostnameCollision_PanelNames(t *testing.T) {
	applied := "mx.example.net"
	derived := &fakeMailSettings{s: &models.ServerSettings{Hostname: "panel.example.com"}}
	withApplied := &fakeMailSettings{s: &models.ServerSettings{Hostname: "panel.example.com", MailHostname: &applied}}
	untidy := &fakeMailSettings{s: &models.ServerSettings{Hostname: " Panel.Example.COM. "}}
	noHostname := &fakeMailSettings{s: &models.ServerSettings{}}

	for _, tc := range []struct {
		name     string
		settings MailSettingsReader
		domain   string
		want     bool
	}{
		{"the panel hostname", derived, "panel.example.com", true},
		{"the derived mail hostname", derived, "mail.panel.example.com", true},
		{"any case, trailing dot", derived, "MAIL.Panel.example.com.", true},
		{"stored hostname untidy", untidy, "mail.panel.example.com", true},
		{"panel hostname with a custom name applied", withApplied, "panel.example.com", true},
		{"derived name still served after a switchover", withApplied, "mail.panel.example.com", true},
		{"a parent zone of the panel hostname", derived, "example.com", false},
		{"a name under the panel hostname", derived, "shop.panel.example.com", false},
		{"a name under the derived name", derived, "a.mail.panel.example.com", false},
		{"www of the panel hostname", derived, "www.panel.example.com", false},
		{"look-alike label", derived, "mailx.panel.example.com", false},
		{"no panel hostname set", noHostname, "mail.panel.example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MailHostnameCollision(context.Background(), tc.settings, tc.domain)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("collision = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMailHostnameCollision_LookupErrorFailsClosed(t *testing.T) {
	boom := errors.New("db down")
	_, err := MailHostnameCollision(context.Background(), &fakeMailSettings{err: boom}, "example.net")
	if !errors.Is(err, boom) {
		t.Fatalf("a settings read error must be returned, got %v", err)
	}
}
