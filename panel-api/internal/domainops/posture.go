package domainops

import (
	"context"
	"errors"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Service-matrix and mail-posture resolution (JAB-279 AC1/AC2, GH #1449,
// GH #1627, GH #1409). Before a domain row is built, every create door decides
// which services the domain runs (web, DNS, mail), which mail posture it takes
// (a provider, or the 'custom' posture of a DNS template), and which SSL mode it
// starts in. The REST create door (createDomainOp, also used by automation) and
// the operator CLI (`jabali domain create`) each carried their own copy of these
// rules. Both now route through the three steps below, which each adapter calls
// at its own point in its sequence:
//
//  1. CheckWebOffOptions — options that need web hosting on a web-off domain.
//  2. ResolveMailPosture — provider default and validation, the DNS-template
//     posture, and the mail-module coercion.
//  3. ResolveServiceMatrix — SSL mode validation, the derived mail flags, the
//     no-service rule, and the DNS-only SSL rule.
//
// Every rejection is a typed sentinel the adapter maps to its own transport.
// The order of the checks inside each step is the REST door's original order.

var (
	// ErrWebOffReverseProxy means a web-off domain asked to be a reverse proxy.
	ErrWebOffReverseProxy = errors.New("domainops: a reverse-proxy domain requires web hosting")
	// ErrWebOffTempURL means a web-off domain asked for a preview URL.
	ErrWebOffTempURL = errors.New("domainops: a preview URL requires web hosting")
	// ErrWebOffDocRoot means a web-off domain was given a document root.
	ErrWebOffDocRoot = errors.New("domainops: a web-disabled domain has no document root")

	// ErrMailProviderInvalid means the requested mail provider is not a known one.
	ErrMailProviderInvalid = errors.New("domainops: invalid mail provider")
	// ErrMailProviderCustomReserved means the caller asked for the 'custom'
	// provider directly. 'custom' is the posture of a DNS-template domain and is
	// set only by the template path; a bare 'custom' would be an inert domain.
	ErrMailProviderCustomReserved = errors.New("domainops: the custom mail provider is set only by a DNS template")
	// ErrDNSTemplatesUnavailable means a template was chosen but this server has
	// no DNS-template store wired.
	ErrDNSTemplatesUnavailable = errors.New("domainops: DNS templates are not enabled")
	// ErrDNSTemplateProviderExclusive means a template and an explicit non-Jabali
	// mail provider were both chosen. The template sets the posture.
	ErrDNSTemplateProviderExclusive = errors.New("domainops: a DNS template and a mail provider are mutually exclusive")
	// ErrDNSTemplateRequiresDNS means a template was chosen for a domain whose DNS
	// is hosted elsewhere, so there is no zone to seed.
	ErrDNSTemplateRequiresDNS = errors.New("domainops: a DNS template requires panel-hosted DNS")
	// ErrDNSTemplateUnknown means the chosen template does not exist.
	ErrDNSTemplateUnknown = errors.New("domainops: DNS template does not exist")
	// ErrDNSTemplateLookup means the template store failed. The store error is
	// carried and printed; errors.Is matches both.
	ErrDNSTemplateLookup = errors.New("domainops: DNS template lookup failed")

	// ErrSSLModeInvalid means the requested SSL mode is not a known one.
	ErrSSLModeInvalid = errors.New("domainops: invalid SSL mode")
	// ErrSSLModeCustomAtCreate means 'custom' was requested at create. A custom
	// certificate is uploaded after the domain exists.
	ErrSSLModeCustomAtCreate = errors.New("domainops: a custom certificate is uploaded after create")
	// ErrSSLModeSharedAtCreate means 'shared' was requested at create. A shared
	// certificate is attached after the domain exists.
	ErrSSLModeSharedAtCreate = errors.New("domainops: a shared certificate is attached after create")
	// ErrSSLNoneWithMail means SSL mode 'none' was requested for a domain that
	// runs Jabali mail, which needs TLS (MTA-STS, autoconfig).
	ErrSSLNoneWithMail = errors.New("domainops: a mail-enabled domain needs TLS")
	// ErrNoServiceSelected means web, DNS, and Jabali mail are all off, which
	// leaves the panel nothing to host.
	ErrNoServiceSelected = errors.New("domainops: select at least one service")
)

// WebOffInput is what CheckWebOffOptions needs to know about a create request.
type WebOffInput struct {
	WebEnabled   bool
	ReverseProxy bool
	TempURL      bool
	DocRoot      string // raw; surrounding whitespace is ignored
}

// CheckWebOffOptions rejects options that need web hosting when web hosting is
// off (GH #1449): a web-off domain is docroot-less, so it cannot be a reverse
// proxy, carry a preview URL, or have a document root. It returns nil when web
// hosting is on.
func CheckWebOffOptions(in WebOffInput) error {
	if in.WebEnabled {
		return nil
	}
	if in.ReverseProxy {
		return ErrWebOffReverseProxy
	}
	if in.TempURL {
		return ErrWebOffTempURL
	}
	if strings.TrimSpace(in.DocRoot) != "" {
		return ErrWebOffDocRoot
	}
	return nil
}

// MailSettingsReader reads the server settings row. repository's
// ServerSettingsRepository satisfies it.
type MailSettingsReader interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
}

// MailModuleEnabled reports whether this server's mail module is on. It fails
// OPEN — a nil reader or an unreadable row counts as on — because a provisioning
// coercion must never block a create.
func MailModuleEnabled(ctx context.Context, settings MailSettingsReader) bool {
	if settings == nil {
		return true
	}
	st, err := settings.Get(ctx)
	if err != nil || st == nil {
		return true
	}
	return st.MailEnabled
}

// DNSTemplateFinder looks up a DNS template. repository's DNSTemplateRepository
// satisfies it.
type DNSTemplateFinder interface {
	FindByID(ctx context.Context, id string) (*models.DNSTemplate, error)
}

// MailPostureInput is the requested mail posture.
type MailPostureInput struct {
	// Provider is the requested mail provider. Empty means Jabali.
	Provider string
	// DNSTemplateID is the chosen DNS template, if any. Surrounding whitespace
	// is ignored.
	DNSTemplateID string
	// DNSEnabled is whether the panel hosts this domain's DNS.
	DNSEnabled bool
	// MailModuleEnabled is this server's mail-module state (MailModuleEnabled).
	MailModuleEnabled bool
}

// MailPosture is the resolved mail posture.
type MailPosture struct {
	// Provider is the provider to store.
	Provider string
	// TemplateID is the chosen DNS template, or nil when none was chosen.
	TemplateID *string
}

// ResolveMailPosture resolves the mail provider a new domain stores:
//
//   - an empty provider means Jabali, and an unknown one is rejected;
//   - 'custom' is never accepted as input (GH #1627);
//   - a chosen DNS template needs a template store, no explicit non-Jabali
//     provider, panel-hosted DNS, and an existing template, and then sets the
//     'custom' posture;
//   - a Jabali provider on a server whose mail module is off becomes 'none'
//     (GH #1409). A template's 'custom' posture is never coerced.
//
// templates may be nil when no template store is wired; that only matters when
// a template is chosen.
func ResolveMailPosture(ctx context.Context, templates DNSTemplateFinder, in MailPostureInput) (MailPosture, error) {
	provider := in.Provider
	if provider == "" {
		provider = models.MailProviderJabali
	}
	if !models.ValidMailProvider(provider) {
		return MailPosture{}, ErrMailProviderInvalid
	}
	if provider == models.MailProviderCustom {
		return MailPosture{}, ErrMailProviderCustomReserved
	}

	var templateID *string
	if id := strings.TrimSpace(in.DNSTemplateID); id != "" {
		if templates == nil {
			return MailPosture{}, ErrDNSTemplatesUnavailable
		}
		if provider != models.MailProviderJabali {
			return MailPosture{}, ErrDNSTemplateProviderExclusive
		}
		if !in.DNSEnabled {
			return MailPosture{}, ErrDNSTemplateRequiresDNS
		}
		if _, err := templates.FindByID(ctx, id); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return MailPosture{}, ErrDNSTemplateUnknown
			}
			return MailPosture{}, &kindError{kind: ErrDNSTemplateLookup, cause: err}
		}
		provider = models.MailProviderCustom
		templateID = &id
	}

	return MailPosture{
		Provider:   MailProviderForServer(provider, in.MailModuleEnabled),
		TemplateID: templateID,
	}, nil
}

// ServiceMatrixInput is the resolved service choice plus the requested SSL mode.
type ServiceMatrixInput struct {
	WebEnabled bool
	DNSEnabled bool
	// MailProvider is the resolved provider (MailPosture.Provider).
	MailProvider string
	// SSLMode is the requested SSL mode. Empty means Let's Encrypt.
	SSLMode string
}

// ServiceMatrix is the resolved SSL mode and mail flags a new domain stores.
type ServiceMatrix struct {
	SSLMode      string
	EmailEnabled bool
	SkipAutoSAN  bool
}

// ResolveServiceMatrix validates the requested SSL mode and derives the mail
// flags:
//
//   - an empty SSL mode means Let's Encrypt; 'custom' and 'shared' are set after
//     create, never at create;
//   - a domain that runs Jabali mail needs TLS, so 'none' is rejected;
//   - a domain must run at least one of web, DNS, or Jabali mail (GH #1449);
//   - a DNS-only domain (web off, no Jabali mail) has nothing to serve over
//     TLS, so its SSL mode is forced to 'none'. A mail-only domain keeps its
//     mode for the mail-support SANs.
func ResolveServiceMatrix(in ServiceMatrixInput) (ServiceMatrix, error) {
	sslMode := in.SSLMode
	if sslMode == "" {
		sslMode = models.SSLModeLE
	}
	if !models.ValidSSLMode(sslMode) {
		return ServiceMatrix{}, ErrSSLModeInvalid
	}
	if sslMode == models.SSLModeCustom {
		return ServiceMatrix{}, ErrSSLModeCustomAtCreate
	}
	if sslMode == models.SSLModeShared {
		return ServiceMatrix{}, ErrSSLModeSharedAtCreate
	}

	emailEnabled, skipAutoSAN := models.DeriveMailFlags(in.MailProvider)
	if sslMode == models.SSLModeNone && emailEnabled {
		return ServiceMatrix{}, ErrSSLNoneWithMail
	}
	if !in.WebEnabled && !in.DNSEnabled && !emailEnabled {
		return ServiceMatrix{}, ErrNoServiceSelected
	}
	if !in.WebEnabled && !emailEnabled {
		sslMode = models.SSLModeNone
	}
	return ServiceMatrix{SSLMode: sslMode, EmailEnabled: emailEnabled, SkipAutoSAN: skipAutoSAN}, nil
}
