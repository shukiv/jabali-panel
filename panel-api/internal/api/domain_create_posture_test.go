package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_create_posture_test.go — JAB-279. Characterizes the create door's
// service-matrix and mail-posture resolution (web-off option guards, mail
// provider + DNS-template posture, mail-module coercion, SSL mode, and the
// no-service / DNS-only rules) at the wire: every rejection's status, code and
// detail, the precedence between rejections, and the stored posture on success.
// The rules now live in domainops; this pins that the REST adapter still emits
// exactly what it emitted when they were inline.

// errDNSTemplates is a DNSTemplateRepository whose lookup always fails with a
// store error (not ErrNotFound).
type errDNSTemplates struct {
	repository.DNSTemplateRepository
}

func (errDNSTemplates) FindByID(context.Context, string) (*models.DNSTemplate, error) {
	return nil, errors.New("dns_templates: connection refused")
}

// errSettingsRepo is a ServerSettingsRepository whose read always fails.
type errSettingsRepo struct {
	repository.ServerSettingsRepository
}

func (errSettingsRepo) Get(context.Context) (*models.ServerSettings, error) {
	return nil, errors.New("server_settings: connection refused")
}

const postureTemplateID = "tmpl-posture"

func postureOwner() *models.User {
	uname := "alice"
	return &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
}

// postureHandler builds a handler with an owner, a domain store, and a DNS
// template repository holding postureTemplateID. mutate adjusts the config.
func postureHandler(owner *models.User, mutate func(*DomainHandlerConfig)) (*domainHandler, *dcDomains) {
	dom := newDCDomains()
	cfg := DomainHandlerConfig{
		Users:   newAbUsers(owner),
		Domains: dom,
		DNSTemplates: fakeDNSTemplates{byID: map[string]*models.DNSTemplate{
			postureTemplateID: {ID: postureTemplateID, Name: "Acme SaaS"},
		}},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return &domainHandler{cfg: cfg}, dom
}

func TestCreateDomainOp_PostureRejections(t *testing.T) {
	owner := postureOwner()
	noTemplates := func(c *DomainHandlerConfig) { c.DNSTemplates = nil }
	failingTemplates := func(c *DomainHandlerConfig) { c.DNSTemplates = errDNSTemplates{} }

	cases := []struct {
		name   string
		mutate func(*DomainHandlerConfig)
		in     createDomainInput
		status int
		code   string
		detail string
	}{
		{
			name:   "web off + reverse proxy",
			in:     createDomainInput{Name: "a.example.com", WebDisabled: true, ReverseProxy: true},
			status: http.StatusBadRequest, code: "web_disabled_no_reverse_proxy",
			detail: "a reverse-proxy domain requires web hosting",
		},
		{
			name:   "web off + preview URL",
			in:     createDomainInput{Name: "a.example.com", WebDisabled: true, TempURLEnabled: true},
			status: http.StatusBadRequest, code: "web_disabled_no_temp_url",
			detail: "a preview URL requires web hosting",
		},
		{
			name:   "web off + document root (whitespace-padded)",
			in:     createDomainInput{Name: "a.example.com", WebDisabled: true, DocRoot: "  /home/alice/site  "},
			status: http.StatusBadRequest, code: "web_disabled_no_docroot",
			detail: "a web-disabled domain has no document root",
		},
		{
			name:   "unknown mail provider",
			in:     createDomainInput{Name: "a.example.com", MailProvider: "exchange"},
			status: http.StatusBadRequest, code: "invalid_mail_provider",
		},
		{
			name:   "caller-supplied custom provider",
			in:     createDomainInput{Name: "a.example.com", MailProvider: models.MailProviderCustom},
			status: http.StatusBadRequest, code: "mail_provider_custom_reserved",
			detail: "select a DNS template via dns_template_id rather than setting mail_provider=custom directly",
		},
		{
			name:   "template with templates unwired",
			mutate: noTemplates,
			in:     createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID},
			status: http.StatusServiceUnavailable, code: "dns_templates_unavailable",
			detail: "DNS templates are not enabled on this server",
		},
		{
			name:   "template + explicit external provider",
			in:     createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID, MailProvider: models.MailProviderGoogle},
			status: http.StatusBadRequest, code: "template_and_provider_exclusive",
			detail: "a DNS template sets the mail posture; do not also select a mail provider",
		},
		{
			name:   "template + external DNS",
			in:     createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID, DNSDisabled: true},
			status: http.StatusBadRequest, code: "template_requires_dns",
			detail: "a DNS template seeds records into the panel-hosted zone; this domain has DNS hosted externally",
		},
		{
			name:   "unknown template id",
			in:     createDomainInput{Name: "a.example.com", DNSTemplateID: "  missing-template  "},
			status: http.StatusBadRequest, code: "unknown_dns_template",
			detail: "the selected DNS template does not exist",
		},
		{
			name:   "template store error",
			mutate: failingTemplates,
			in:     createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID},
			status: http.StatusInternalServerError, code: "dns_template_lookup_failed",
		},
		{
			name:   "unknown ssl mode",
			in:     createDomainInput{Name: "a.example.com", SSLMode: "bogus"},
			status: http.StatusBadRequest, code: "invalid_ssl_mode",
		},
		{
			name:   "custom ssl mode at create",
			in:     createDomainInput{Name: "a.example.com", SSLMode: models.SSLModeCustom},
			status: http.StatusBadRequest, code: "ssl_mode_custom_requires_upload",
			detail: "create the domain with le/self/none, then upload a custom cert via the SSL settings",
		},
		{
			name:   "shared ssl mode at create",
			in:     createDomainInput{Name: "a.example.com", SSLMode: models.SSLModeShared},
			status: http.StatusBadRequest, code: "ssl_mode_shared_requires_attach",
			detail: "create the domain with le/self/none, then attach a shared cert via POST /domains/:id/ssl/shared",
		},
		{
			name:   "no TLS on a mail-enabled domain",
			in:     createDomainInput{Name: "a.example.com", SSLMode: models.SSLModeNone, MailProvider: models.MailProviderJabali},
			status: http.StatusBadRequest, code: "ssl_none_with_email",
			detail: "a mail-enabled domain needs TLS; choose le/self or set mail provider to none",
		},
		{
			name:   "web off + DNS off + no mail",
			in:     createDomainInput{Name: "a.example.com", WebDisabled: true, DNSDisabled: true, MailProvider: models.MailProviderNone},
			status: http.StatusBadRequest, code: "no_service_selected",
			detail: "select at least one service: web hosting, DNS, or mail",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, dom := postureHandler(owner, tc.mutate)
			tc.in.OwnerID = owner.ID
			tc.in.SkipInlineSSL = true
			d, oerr := createDomainOp(context.Background(), h, tc.in)
			if oerr == nil {
				t.Fatalf("expected %s, got success (%+v)", tc.code, d)
			}
			if oerr.Status != tc.status || oerr.Code != tc.code || oerr.Detail != tc.detail {
				t.Fatalf("got {%d %q %q}, want {%d %q %q}", oerr.Status, oerr.Code, oerr.Detail, tc.status, tc.code, tc.detail)
			}
			if len(dom.created) != 0 {
				t.Fatalf("a rejected create must persist nothing, got %d rows", len(dom.created))
			}
		})
	}
}

// TestCreateDomainOp_PosturePrecedence pins which rejection wins when a request
// breaks more than one rule, so moving the rules into domainops cannot reorder
// them.
func TestCreateDomainOp_PosturePrecedence(t *testing.T) {
	owner := postureOwner()
	noTemplates := func(c *DomainHandlerConfig) { c.DNSTemplates = nil }
	withWebTemplates := func(c *DomainHandlerConfig) {
		c.WebTemplates = fakeWebTemplates{byID: map[string]*models.WebTemplate{}}
	}
	mailModuleOff := func(c *DomainHandlerConfig) {
		c.ServerSettings = &fakeStatusSettingsRepo{s: &models.ServerSettings{MailEnabled: false}}
	}

	cases := []struct {
		name   string
		mutate func(*DomainHandlerConfig)
		in     createDomainInput
		code   string
	}{
		{"reverse proxy before preview URL", nil,
			createDomainInput{Name: "a.example.com", WebDisabled: true, ReverseProxy: true, TempURLEnabled: true, DocRoot: "/x"},
			"web_disabled_no_reverse_proxy"},
		{"preview URL before document root", nil,
			createDomainInput{Name: "a.example.com", WebDisabled: true, TempURLEnabled: true, DocRoot: "/x"},
			"web_disabled_no_temp_url"},
		{"web-off guards before the mail provider", nil,
			createDomainInput{Name: "a.example.com", WebDisabled: true, DocRoot: "/x", MailProvider: "exchange"},
			"web_disabled_no_docroot"},
		{"mail provider before SSL mode", nil,
			createDomainInput{Name: "a.example.com", MailProvider: "exchange", SSLMode: "bogus"},
			"invalid_mail_provider"},
		{"unwired templates before provider exclusivity", noTemplates,
			createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID, MailProvider: models.MailProviderM365},
			"dns_templates_unavailable"},
		{"provider exclusivity before the DNS requirement", nil,
			createDomainInput{Name: "a.example.com", DNSTemplateID: "missing", MailProvider: models.MailProviderM365, DNSDisabled: true},
			"template_and_provider_exclusive"},
		{"DNS requirement before the template lookup", nil,
			createDomainInput{Name: "a.example.com", DNSTemplateID: "missing", DNSDisabled: true},
			"template_requires_dns"},
		{"mail posture before the web template gate", withWebTemplates,
			createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID, MailProvider: models.MailProviderM365, WebTemplateID: "wt"},
			"template_and_provider_exclusive"},
		{"M365 tenant before SSL mode", nil,
			createDomainInput{Name: "a.example.com", M365Onmicrosoft: "not a tenant!", SSLMode: "bogus"},
			"invalid_m365_onmicrosoft"},
		{"SSL mode before the service matrix", nil,
			createDomainInput{Name: "a.example.com", SSLMode: models.SSLModeCustom, WebDisabled: true, DNSDisabled: true, MailProvider: models.MailProviderNone},
			"ssl_mode_custom_requires_upload"},
		{"coercion runs before the no-service check", mailModuleOff,
			createDomainInput{Name: "a.example.com", WebDisabled: true, DNSDisabled: true, MailProvider: models.MailProviderJabali},
			"no_service_selected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := postureHandler(owner, tc.mutate)
			tc.in.OwnerID = owner.ID
			tc.in.SkipInlineSSL = true
			_, oerr := createDomainOp(context.Background(), h, tc.in)
			if oerr == nil || oerr.Code != tc.code {
				t.Fatalf("want %s, got %+v", tc.code, oerr)
			}
		})
	}
}

// TestCreateDomainOp_PostureOutcomes pins the stored posture of an accepted
// create.
func TestCreateDomainOp_PostureOutcomes(t *testing.T) {
	owner := postureOwner()
	mailModule := func(on bool) func(*DomainHandlerConfig) {
		return func(c *DomainHandlerConfig) {
			c.ServerSettings = &fakeStatusSettingsRepo{s: &models.ServerSettings{MailEnabled: on}}
		}
	}
	unreadableSettings := func(c *DomainHandlerConfig) { c.ServerSettings = errSettingsRepo{} }

	type want struct {
		provider    string
		templateID  string
		email       bool
		skipAutoSAN bool
		sslMode     string
	}
	cases := []struct {
		name   string
		mutate func(*DomainHandlerConfig)
		in     createDomainInput
		want   want
	}{
		{"defaults: jabali mail, le", nil,
			createDomainInput{Name: "a.example.com"},
			want{provider: models.MailProviderJabali, email: true, sslMode: models.SSLModeLE}},
		{"external provider keeps TLS, no local mail", nil,
			createDomainInput{Name: "a.example.com", MailProvider: models.MailProviderGoogle, SSLMode: models.SSLModeSelf},
			want{provider: models.MailProviderGoogle, skipAutoSAN: true, sslMode: models.SSLModeSelf}},
		{"template id is trimmed and forces the custom posture", nil,
			createDomainInput{Name: "a.example.com", DNSTemplateID: "  " + postureTemplateID + "  "},
			want{provider: models.MailProviderCustom, templateID: postureTemplateID, skipAutoSAN: true, sslMode: models.SSLModeLE}},
		{"custom posture is not coerced on a mail-less server", mailModule(false),
			createDomainInput{Name: "a.example.com", DNSTemplateID: postureTemplateID},
			want{provider: models.MailProviderCustom, templateID: postureTemplateID, skipAutoSAN: true, sslMode: models.SSLModeLE}},
		{"jabali is coerced to none on a mail-less server", mailModule(false),
			createDomainInput{Name: "a.example.com"},
			want{provider: models.MailProviderNone, skipAutoSAN: true, sslMode: models.SSLModeLE}},
		{"an unreadable settings row fails open (jabali kept)", unreadableSettings,
			createDomainInput{Name: "a.example.com"},
			want{provider: models.MailProviderJabali, email: true, sslMode: models.SSLModeLE}},
		{"DNS-only forces ssl none even when self was requested", nil,
			createDomainInput{Name: "a.example.com", WebDisabled: true, MailProvider: models.MailProviderNone, SSLMode: models.SSLModeSelf},
			want{provider: models.MailProviderNone, skipAutoSAN: true, sslMode: models.SSLModeNone}},
		{"mail-only keeps the requested mode for the mail SANs", nil,
			createDomainInput{Name: "a.example.com", WebDisabled: true, SSLMode: models.SSLModeSelf},
			want{provider: models.MailProviderJabali, email: true, sslMode: models.SSLModeSelf}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, dom := postureHandler(owner, tc.mutate)
			tc.in.OwnerID = owner.ID
			tc.in.SkipInlineSSL = true
			d, oerr := createDomainOp(context.Background(), h, tc.in)
			if oerr != nil {
				t.Fatalf("unexpected error: %+v", oerr)
			}
			if len(dom.created) != 1 {
				t.Fatalf("want 1 persisted row, got %d", len(dom.created))
			}
			gotTemplate := ""
			if d.MailTemplateID != nil {
				gotTemplate = *d.MailTemplateID
			}
			got := want{d.MailProvider, gotTemplate, d.EmailEnabled, d.SkipAutoSAN, d.SSLMode}
			if got != tc.want {
				t.Fatalf("posture = %+v, want %+v", got, tc.want)
			}
			if d.SSLEnabled != models.SSLEnabledForMode(tc.want.sslMode) {
				t.Fatalf("SSLEnabled = %v, want %v for mode %q", d.SSLEnabled, models.SSLEnabledForMode(tc.want.sslMode), tc.want.sslMode)
			}
		})
	}
}
