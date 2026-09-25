package domainops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestCheckWebOffOptions(t *testing.T) {
	cases := []struct {
		name string
		in   WebOffInput
		want error
	}{
		{"web on allows every option", WebOffInput{WebEnabled: true, ReverseProxy: true, TempURL: true, DocRoot: "/home/a/x"}, nil},
		{"web off, no options", WebOffInput{}, nil},
		{"web off, whitespace-only docroot", WebOffInput{DocRoot: "  \t "}, nil},
		{"web off + reverse proxy", WebOffInput{ReverseProxy: true}, ErrWebOffReverseProxy},
		{"web off + preview URL", WebOffInput{TempURL: true}, ErrWebOffTempURL},
		{"web off + docroot", WebOffInput{DocRoot: " /home/a/x "}, ErrWebOffDocRoot},
		{"reverse proxy wins over preview URL and docroot", WebOffInput{ReverseProxy: true, TempURL: true, DocRoot: "/x"}, ErrWebOffReverseProxy},
		{"preview URL wins over docroot", WebOffInput{TempURL: true, DocRoot: "/x"}, ErrWebOffTempURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckWebOffOptions(tc.in); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

type fakeSettings struct {
	st  *models.ServerSettings
	err error
}

func (f fakeSettings) Get(context.Context) (*models.ServerSettings, error) { return f.st, f.err }

func TestMailModuleEnabled(t *testing.T) {
	ctx := context.Background()
	if !MailModuleEnabled(ctx, nil) {
		t.Error("a nil settings reader must fail open (module on)")
	}
	if !MailModuleEnabled(ctx, fakeSettings{err: errors.New("db down")}) {
		t.Error("an unreadable settings row must fail open (module on)")
	}
	if !MailModuleEnabled(ctx, fakeSettings{}) {
		t.Error("a missing settings row must fail open (module on)")
	}
	if !MailModuleEnabled(ctx, fakeSettings{st: &models.ServerSettings{MailEnabled: true}}) {
		t.Error("module on must read as on")
	}
	if MailModuleEnabled(ctx, fakeSettings{st: &models.ServerSettings{MailEnabled: false}}) {
		t.Error("module off must read as off")
	}
}

type fakeTemplateFinder struct {
	known map[string]bool
	err   error
	ids   []string
}

func (f *fakeTemplateFinder) FindByID(_ context.Context, id string) (*models.DNSTemplate, error) {
	f.ids = append(f.ids, id)
	if f.err != nil {
		return nil, f.err
	}
	if f.known[id] {
		return &models.DNSTemplate{ID: id}, nil
	}
	return nil, repository.ErrNotFound
}

func TestResolveMailPosture(t *testing.T) {
	const tmpl = "tmpl-1"
	on := MailPostureInput{DNSEnabled: true, MailModuleEnabled: true}
	with := func(base MailPostureInput, mutate func(*MailPostureInput)) MailPostureInput {
		mutate(&base)
		return base
	}

	cases := []struct {
		name         string
		noFinder     bool
		in           MailPostureInput
		wantErr      error
		wantProvider string
		wantTemplate string
		wantLookups  int
	}{
		{name: "empty provider means jabali", in: on, wantProvider: models.MailProviderJabali},
		{name: "empty provider on a mail-less server is none",
			in:           with(on, func(i *MailPostureInput) { i.MailModuleEnabled = false }),
			wantProvider: models.MailProviderNone},
		{name: "external provider is kept on a mail-less server",
			in:           with(on, func(i *MailPostureInput) { i.Provider = models.MailProviderM365; i.MailModuleEnabled = false }),
			wantProvider: models.MailProviderM365},
		{name: "none stays none",
			in:           with(on, func(i *MailPostureInput) { i.Provider = models.MailProviderNone }),
			wantProvider: models.MailProviderNone},
		{name: "unknown provider",
			in:      with(on, func(i *MailPostureInput) { i.Provider = "exchange" }),
			wantErr: ErrMailProviderInvalid},
		{name: "custom is never accepted as input",
			in:      with(on, func(i *MailPostureInput) { i.Provider = models.MailProviderCustom }),
			wantErr: ErrMailProviderCustomReserved},
		{name: "template with no store wired", noFinder: true,
			in:      with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl }),
			wantErr: ErrDNSTemplatesUnavailable},
		{name: "no store wins over provider exclusivity", noFinder: true,
			in:      with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl; i.Provider = models.MailProviderGoogle }),
			wantErr: ErrDNSTemplatesUnavailable},
		{name: "template + external provider",
			in:      with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl; i.Provider = models.MailProviderGoogle }),
			wantErr: ErrDNSTemplateProviderExclusive},
		{name: "template + provider none is also exclusive",
			in:      with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl; i.Provider = models.MailProviderNone }),
			wantErr: ErrDNSTemplateProviderExclusive},
		{name: "exclusivity wins over the DNS requirement",
			in: with(on, func(i *MailPostureInput) {
				i.DNSTemplateID = tmpl
				i.Provider = models.MailProviderGoogle
				i.DNSEnabled = false
			}),
			wantErr: ErrDNSTemplateProviderExclusive},
		{name: "template + external DNS, checked before the lookup",
			in:      with(on, func(i *MailPostureInput) { i.DNSTemplateID = "missing"; i.DNSEnabled = false }),
			wantErr: ErrDNSTemplateRequiresDNS},
		{name: "unknown template",
			in:          with(on, func(i *MailPostureInput) { i.DNSTemplateID = "missing" }),
			wantErr:     ErrDNSTemplateUnknown,
			wantLookups: 1},
		{name: "template sets the custom posture; the id is trimmed",
			in:           with(on, func(i *MailPostureInput) { i.DNSTemplateID = "  " + tmpl + "  " }),
			wantProvider: models.MailProviderCustom, wantTemplate: tmpl, wantLookups: 1},
		{name: "template with an explicit jabali provider",
			in:           with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl; i.Provider = models.MailProviderJabali }),
			wantProvider: models.MailProviderCustom, wantTemplate: tmpl, wantLookups: 1},
		{name: "template posture is not coerced on a mail-less server",
			in:           with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl; i.MailModuleEnabled = false }),
			wantProvider: models.MailProviderCustom, wantTemplate: tmpl, wantLookups: 1},
		{name: "whitespace-only template id means no template",
			in:           with(on, func(i *MailPostureInput) { i.DNSTemplateID = "   " }),
			wantProvider: models.MailProviderJabali},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			finder := &fakeTemplateFinder{known: map[string]bool{tmpl: true}}
			var templates DNSTemplateFinder = finder
			if tc.noFinder {
				templates = nil
			}
			got, err := ResolveMailPosture(context.Background(), templates, tc.in)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(finder.ids) != tc.wantLookups {
				t.Fatalf("template lookups = %q, want %d", finder.ids, tc.wantLookups)
			}
			if tc.wantLookups > 0 && finder.ids[0] != strings.TrimSpace(tc.in.DNSTemplateID) {
				t.Fatalf("looked up %q, want the trimmed id", finder.ids[0])
			}
			if err != nil {
				return
			}
			if got.Provider != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", got.Provider, tc.wantProvider)
			}
			gotTemplate := ""
			if got.TemplateID != nil {
				gotTemplate = *got.TemplateID
			}
			if gotTemplate != tc.wantTemplate {
				t.Fatalf("template = %q, want %q", gotTemplate, tc.wantTemplate)
			}
		})
	}

	t.Run("template store error carries the cause without a package prefix", func(t *testing.T) {
		cause := errors.New("dns_templates: connection refused")
		_, err := ResolveMailPosture(context.Background(), &fakeTemplateFinder{err: cause},
			with(on, func(i *MailPostureInput) { i.DNSTemplateID = tmpl }))
		if !errors.Is(err, ErrDNSTemplateLookup) || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want both ErrDNSTemplateLookup and the store error", err)
		}
		if err.Error() != cause.Error() {
			t.Fatalf("Error() = %q, want the store error text %q", err.Error(), cause.Error())
		}
	})
}

func TestResolveServiceMatrix(t *testing.T) {
	full := ServiceMatrixInput{WebEnabled: true, DNSEnabled: true, MailProvider: models.MailProviderJabali}
	with := func(mutate func(*ServiceMatrixInput)) ServiceMatrixInput {
		in := full
		mutate(&in)
		return in
	}

	cases := []struct {
		name    string
		in      ServiceMatrixInput
		wantErr error
		want    ServiceMatrix
	}{
		{name: "empty mode means Let's Encrypt", in: full,
			want: ServiceMatrix{SSLMode: models.SSLModeLE, EmailEnabled: true}},
		{name: "self is kept", in: with(func(i *ServiceMatrixInput) { i.SSLMode = models.SSLModeSelf }),
			want: ServiceMatrix{SSLMode: models.SSLModeSelf, EmailEnabled: true}},
		{name: "external provider: no local mail, skip auto SANs",
			in:   with(func(i *ServiceMatrixInput) { i.MailProvider = models.MailProviderGoogle }),
			want: ServiceMatrix{SSLMode: models.SSLModeLE, SkipAutoSAN: true}},
		{name: "none is allowed without Jabali mail",
			in:   with(func(i *ServiceMatrixInput) { i.MailProvider = models.MailProviderNone; i.SSLMode = models.SSLModeNone }),
			want: ServiceMatrix{SSLMode: models.SSLModeNone, SkipAutoSAN: true}},
		{name: "unknown mode", in: with(func(i *ServiceMatrixInput) { i.SSLMode = "bogus" }), wantErr: ErrSSLModeInvalid},
		{name: "custom at create", in: with(func(i *ServiceMatrixInput) { i.SSLMode = models.SSLModeCustom }), wantErr: ErrSSLModeCustomAtCreate},
		{name: "shared at create", in: with(func(i *ServiceMatrixInput) { i.SSLMode = models.SSLModeShared }), wantErr: ErrSSLModeSharedAtCreate},
		{name: "none with Jabali mail", in: with(func(i *ServiceMatrixInput) { i.SSLMode = models.SSLModeNone }), wantErr: ErrSSLNoneWithMail},
		{name: "nothing to host",
			in:      ServiceMatrixInput{MailProvider: models.MailProviderNone},
			wantErr: ErrNoServiceSelected},
		{name: "external mail alone is nothing to host",
			in:      ServiceMatrixInput{MailProvider: models.MailProviderM365},
			wantErr: ErrNoServiceSelected},
		{name: "SSL mode is checked before the service rules",
			in:      ServiceMatrixInput{MailProvider: models.MailProviderNone, SSLMode: models.SSLModeShared},
			wantErr: ErrSSLModeSharedAtCreate},
		{name: "DNS-only is forced to none",
			in:   ServiceMatrixInput{DNSEnabled: true, MailProvider: models.MailProviderNone, SSLMode: models.SSLModeSelf},
			want: ServiceMatrix{SSLMode: models.SSLModeNone, SkipAutoSAN: true}},
		{name: "mail-only keeps its mode for the mail SANs",
			in:   ServiceMatrixInput{MailProvider: models.MailProviderJabali, SSLMode: models.SSLModeSelf},
			want: ServiceMatrix{SSLMode: models.SSLModeSelf, EmailEnabled: true}},
		{name: "web-only with external DNS keeps Let's Encrypt",
			in:   ServiceMatrixInput{WebEnabled: true, MailProvider: models.MailProviderNone},
			want: ServiceMatrix{SSLMode: models.SSLModeLE, SkipAutoSAN: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveServiceMatrix(tc.in)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("matrix = %+v, want %+v", got, tc.want)
			}
		})
	}
}
