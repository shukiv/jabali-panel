package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// postureTemplates is a DNSTemplateFinder for the CLI posture-message tests.
type postureTemplates struct {
	known map[string]bool
	err   error
}

func (f postureTemplates) FindByID(_ context.Context, id string) (*models.DNSTemplate, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.known[id] {
		return &models.DNSTemplate{ID: id}, nil
	}
	return nil, repository.ErrNotFound
}

// TestCLIMailPostureError_Messages drives the shared domainops.ResolveMailPosture
// with each rejecting CLI input and pins that `jabali domain create` prints the
// exact message it printed when the rules were inline (JAB-279).
func TestCLIMailPostureError_Messages(t *testing.T) {
	templates := postureTemplates{known: map[string]bool{"tmpl-1": true}}
	cases := []struct {
		name      string
		templates domainops.DNSTemplateFinder
		in        cliDomainInput
		dnsOff    bool
		want      string
	}{
		{name: "unknown provider", templates: templates,
			in:   cliDomainInput{MailProvider: "exchange"},
			want: `invalid --mail "exchange" (want jabali|none|m365|google)`},
		{name: "custom provider", templates: templates,
			in:   cliDomainInput{MailProvider: "custom"},
			want: `invalid --mail "custom" (custom is the posture of a domain created from a DNS template; not selectable on the CLI)`},
		{name: "template + provider", templates: templates,
			in:   cliDomainInput{MailProvider: "google", DNSTemplateID: "tmpl-1"},
			want: `--dns-template sets the mail posture; do not also pass --mail "google"`},
		{name: "template + external DNS", templates: templates,
			in: cliDomainInput{DNSTemplateID: "tmpl-1"}, dnsOff: true,
			want: `--dns-template seeds records into the panel-hosted zone; --manage-dns=false hosts DNS externally`},
		{name: "unknown template (padded)", templates: templates,
			in:   cliDomainInput{DNSTemplateID: "  missing  "},
			want: `--dns-template "missing" does not exist`},
		{name: "template store error", templates: postureTemplates{err: errors.New("connection refused")},
			in:   cliDomainInput{DNSTemplateID: "tmpl-1"},
			want: `look up DNS template: connection refused`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domainops.ResolveMailPosture(context.Background(), tc.templates, domainops.MailPostureInput{
				Provider:          tc.in.MailProvider,
				DNSTemplateID:     tc.in.DNSTemplateID,
				DNSEnabled:        !tc.dnsOff,
				MailModuleEnabled: true,
			})
			if err == nil {
				t.Fatal("expected a posture rejection")
			}
			if got := cliMailPostureError(err, tc.in).Error(); got != tc.want {
				t.Fatalf("message = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("an unmapped error passes through", func(t *testing.T) {
		other := errors.New("other")
		if got := cliMailPostureError(other, cliDomainInput{}); got != other {
			t.Fatalf("got %v, want the original error", got)
		}
	})
}

// TestCLICreateDomain_WiresServiceMatrix source-pins that `jabali domain create`
// resolves the web-off guards, the SSL mode, and the mail flags through the
// shared domainops steps and stores the result, with the CLI's own messages.
// createDomainDirect calls initConfig / initDB and is not unit-testable, so the
// source-pin is the load-bearing guard; the rules are proven by
// domainops.TestCheckWebOffOptions and domainops.TestResolveServiceMatrix.
func TestCLICreateDomain_WiresServiceMatrix(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	for _, want := range []string{
		"domainops.ResolveServiceMatrix(domainops.ServiceMatrixInput{",
		"errors.Is(err, domainops.ErrNoServiceSelected)",
		`"select at least one service: web hosting (--web-enabled), DNS (--manage-dns), or mail (--mail)"`,
		"domainops.CheckWebOffOptions(domainops.WebOffInput{",
		"errors.Is(err, domainops.ErrWebOffReverseProxy)",
		`"a reverse-proxy domain requires web hosting"`,
		"errors.Is(err, domainops.ErrWebOffDocRoot)",
		`"a web-disabled domain has no document root"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("CLI create must contain %s", want)
		}
	}
	for _, re := range []string{
		`EmailEnabled:\s+matrix\.EmailEnabled,`,
		`SkipAutoSAN:\s+matrix\.SkipAutoSAN,`,
		`SSLMode:\s+matrix\.SSLMode,`,
		`SSLEnabled:\s+models\.SSLEnabledForMode\(matrix\.SSLMode\),`,
	} {
		if !regexp.MustCompile(re).MatchString(src) {
			t.Errorf("CLI create must store the resolved service matrix (%s)", re)
		}
	}
	// No inline SSL-mode decision in the adapter.
	if strings.Contains(src, "sslMode = models.SSLModeNone") {
		t.Error("CLI create must not force the DNS-only SSL mode inline; ResolveServiceMatrix owns it")
	}
}
