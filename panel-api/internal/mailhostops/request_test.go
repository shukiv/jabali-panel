package mailhostops

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390 setter. A request is validated, refused for the same readiness and
// name reasons the engine fails an attempt with, and only then recorded as a
// pending switchover. Nothing is applied here: the reconciler applies the
// name once its certificate is issued.

type fakeSettings struct {
	s   *models.ServerSettings
	err error
}

func (f *fakeSettings) Get(context.Context) (*models.ServerSettings, error) { return f.s, f.err }

type fakeCerts struct{ host *models.PanelCertificate }

func (f *fakeCerts) GetByKind(_ context.Context, kind string) (*models.PanelCertificate, error) {
	if kind == models.PanelCertKindHostname && f.host != nil {
		return f.host, nil
	}
	return nil, repository.ErrNotFound
}

type fakeRequestDomains struct {
	fakeDomains
	primary *models.Domain
}

func (f *fakeRequestDomains) FindPanelPrimary(context.Context) (*models.Domain, error) {
	if f.primary == nil {
		return nil, repository.ErrPanelPrimaryNotFound
	}
	return f.primary, nil
}

type fakeSwitchover struct {
	repository.MailHostnameSwitchoverRepository
	requested []string
	by        []string
	reqErr    error
	cancelErr error
	cancels   int
}

func (f *fakeSwitchover) Request(_ context.Context, desired, requestedBy string, _ time.Time) error {
	if f.reqErr != nil {
		return f.reqErr
	}
	f.requested = append(f.requested, desired)
	f.by = append(f.by, requestedBy)
	return nil
}

func (f *fakeSwitchover) Cancel(context.Context, time.Time) error {
	f.cancels++
	return f.cancelErr
}

func requestDeps() (RequestDeps, *fakeSettings, *fakeCerts, *fakeRequestDomains, *fakeSwitchover) {
	s := &fakeSettings{s: &models.ServerSettings{Hostname: "panel.example.com", AdminEmail: "admin@example.com", WebmailEnabled: true}}
	c := &fakeCerts{host: &models.PanelCertificate{Kind: models.PanelCertKindHostname, UseLE: true}}
	primary := &models.Domain{Name: "panel.example.com", IsPanelPrimary: true, EmailEnabled: true, WebmailEnabled: true}
	d := &fakeRequestDomains{fakeDomains: fakeDomains{byName: map[string]*models.Domain{
		"panel.example.com": primary,
		"tenant.net":        {Name: "tenant.net"},
	}}, primary: primary}
	sw := &fakeSwitchover{}
	return RequestDeps{Settings: s, PanelCerts: c, Domains: d, Switchover: sw}, s, c, d, sw
}

func TestRequest_RecordsNormalizedName(t *testing.T) {
	deps, _, _, _, sw := requestDeps()
	got, err := Request(context.Background(), deps, "  MX.Example.ORG ", "admin:u1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if got != "mx.example.org" || len(sw.requested) != 1 || sw.requested[0] != "mx.example.org" || sw.by[0] != "admin:u1" {
		t.Fatalf("recorded %v by %v (returned %q), want mx.example.org by admin:u1", sw.requested, sw.by, got)
	}
}

func TestRequest_ResetToDerivedIsAccepted(t *testing.T) {
	deps, s, _, _, sw := requestDeps()
	applied := "mx.example.org"
	s.s.MailHostname = &applied
	if _, err := Request(context.Background(), deps, "mail.panel.example.com", "admin:u1"); err != nil {
		t.Fatalf("switching back to the derived name must be accepted: %v", err)
	}
	if len(sw.requested) != 1 {
		t.Fatalf("want the reset recorded, got %v", sw.requested)
	}
}

func TestRequest_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		desired string
		mutate  func(*fakeSettings, *fakeCerts, *fakeRequestDomains, *fakeSwitchover)
		kind    error
		cause   error
	}{
		{"invalid", "https://mx.example.org/", nil, ErrInvalidName, models.ErrMailHostnameNotBare},
		{"empty", "", nil, ErrInvalidName, models.ErrMailHostnameEmpty},
		{"self-signed panel", "mx.example.org", func(_ *fakeSettings, c *fakeCerts, _ *fakeRequestDomains, _ *fakeSwitchover) { c.host.UseLE = false }, ErrNotReady, ErrLetsEncryptOff},
		{"no panel-primary", "mx.example.org", func(_ *fakeSettings, _ *fakeCerts, d *fakeRequestDomains, _ *fakeSwitchover) { d.primary = nil }, ErrNotReady, ErrPanelMailOff},
		{"no settings row", "mx.example.org", func(s *fakeSettings, _ *fakeCerts, _ *fakeRequestDomains, _ *fakeSwitchover) {
			s.s, s.err = nil, repository.ErrNotFound
		}, ErrNotReady, ErrPanelIdentityMissing},
		{"already the derived name", "mail.panel.example.com", nil, ErrNameRefused, ErrNameAlreadyApplied},
		{"already the applied name", "mx.example.org", func(s *fakeSettings, _ *fakeCerts, _ *fakeRequestDomains, _ *fakeSwitchover) {
			a := "mx.example.org"
			s.s.MailHostname = &a
		}, ErrNameRefused, ErrNameAlreadyApplied},
		{"panel hostname", "panel.example.com", nil, ErrNameRefused, ErrNameIsPanelHostname},
		{"tenant zone", "mx.tenant.net", nil, ErrNameRefused, ErrNameClaimedByDomain},
		{"tenant domain under it", "mx.example.org", func(_ *fakeSettings, _ *fakeCerts, d *fakeRequestDomains, _ *fakeSwitchover) {
			d.byName["login.mx.example.org"] = &models.Domain{Name: "login.mx.example.org"}
		}, ErrNameRefused, ErrNameClaimedByDomain},
		{"in flight", "mx.example.org", func(_ *fakeSettings, _ *fakeCerts, _ *fakeRequestDomains, sw *fakeSwitchover) {
			sw.reqErr = repository.ErrSwitchoverInFlight
		}, repository.ErrSwitchoverInFlight, repository.ErrSwitchoverInFlight},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, s, c, d, sw := requestDeps()
			if tc.mutate != nil {
				tc.mutate(s, c, d, sw)
			}
			_, err := Request(context.Background(), deps, tc.desired, "admin:u1")
			if !errors.Is(err, tc.kind) || !errors.Is(err, tc.cause) {
				t.Fatalf("Request = %v, want kind %v and cause %v", err, tc.kind, tc.cause)
			}
			if tc.kind != repository.ErrSwitchoverInFlight && len(sw.requested) != 0 {
				t.Fatalf("a refused request must not be recorded, got %v", sw.requested)
			}
		})
	}
}

func TestRequest_AliasUnderNameIsRefused(t *testing.T) {
	deps, _, _, _, sw := requestDeps()
	deps.Aliases = &fakeAliases{held: map[string]bool{"login.mx.example.org": true}}
	_, err := Request(context.Background(), deps, "mx.example.org", "admin:u1")
	if !errors.Is(err, ErrNameRefused) || !errors.Is(err, ErrNameHasAliasUnder) {
		t.Fatalf("Request = %v, want a refusal for the alias under the name", err)
	}
	if len(sw.requested) != 0 {
		t.Fatalf("a refused request must not be recorded, got %v", sw.requested)
	}
}

func TestRequest_LookupErrorsAreNotRefusals(t *testing.T) {
	deps, s, _, _, sw := requestDeps()
	boom := errors.New("db down")
	s.s, s.err = nil, boom
	_, err := Request(context.Background(), deps, "mx.example.org", "admin:u1")
	if !errors.Is(err, boom) || errors.Is(err, ErrNotReady) || errors.Is(err, ErrNameRefused) {
		t.Fatalf("a settings read error must surface as itself, got %v", err)
	}
	if len(sw.requested) != 0 {
		t.Fatal("nothing may be recorded when a lookup failed")
	}
}

func TestCancel(t *testing.T) {
	deps, _, _, _, sw := requestDeps()
	if err := Cancel(context.Background(), deps); err != nil || sw.cancels != 1 {
		t.Fatalf("Cancel = %v (cancels %d)", err, sw.cancels)
	}
	sw.cancelErr = repository.ErrSwitchoverInFlight
	if err := Cancel(context.Background(), deps); !errors.Is(err, repository.ErrSwitchoverInFlight) {
		t.Fatalf("Cancel while issuing = %v", err)
	}
}
