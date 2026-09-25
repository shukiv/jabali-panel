package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// domain_create_op.go — the REST adapter over domainops.Create (JAB-279 AC1).
// JAB-233 first extracted the create orchestration from domainHandler.create so
// the billing automation (userCreateHandler) could create a primary domain with
// the exact GUI semantics; the orchestration itself now lives in the domain
// lifecycle module, which `jabali domain create` shares. What stays here is
// the transport: the handler's dependencies, the post-create hooks this
// process can run (it holds the in-process reconciler), the logging of soft
// fast-path failures, and the mapping of every module rejection to the exact
// HTTP status, code and detail the door has always returned.

// createDomainInput is the pre-normalized, pre-validated-shape input to
// createDomainOp. Name MUST already be normalized (normalizeDomainName) and
// HTML-stripped by the caller — the op validates it but does not re-normalize,
// matching the create() source of truth.

type createDomainInput struct {
	OwnerID string
	Name    string
	DocRoot string // "" → derived under /home/<user>/domains/<name>/public_html
	// ActorIsAdmin is the CALLER's privilege (not the owner's). It selects
	// the document-root confinement: an admin actor may point the docroot
	// anywhere under the owner's home (validateDocumentRoot), while a
	// non-admin actor — a tenant creating their own domain, including via
	// the Add-domain drawer (GH #1413) — is held to the domain's OWN tree
	// (validateTenantDocumentRoot), the same rule the edit path enforces
	// (GH #526). Zero value is false = strict = fail-closed, so callers that
	// never set a custom docroot (automation) need not set it.
	ActorIsAdmin    bool
	MailProvider    string // "" → jabali
	M365Onmicrosoft string
	GoogleDKIM      string
	// DNSTemplateID (GH #1627) is a custom DNS template the tenant selected at
	// create. When set it OVERRIDES the mail posture to external ('custom') and
	// the reconciler seeds the template's records into the fresh zone. Empty for
	// every non-template create. Mutually exclusive with an explicit mail
	// provider, and requires the panel to host DNS (DNSDisabled=false).
	DNSTemplateID string
	// WebTemplateID (GH #1624 / ADR-0169 Phase 3) is an admin web (nginx)
	// template selected at create. ADMIN-ONLY: a non-admin actor naming one is
	// rejected (web_template_admin_only). When set, the template's directives are
	// snapshot-copied onto the new domain's NginxCustomDirectives. Empty for every
	// non-template create.
	WebTemplateID  string
	SSLMode        string // "" → le
	CreateWWW      bool
	TempURLEnabled bool
	// ReverseProxy (GH #1175): make this a reverse-proxy domain — the panel
	// allocates a loopback port and the vhost proxies `/` to it. No DocRoot/PHP.
	ReverseProxy bool
	// ReverseProxyPort (GH #1401): the tenant's chosen loopback port. 0 = the
	// panel auto-assigns from its pool (the #1175 default).
	ReverseProxyPort uint32
	// SkipInlineSSL omits the 30s inline ACME/self-signed attempt (JAB-233):
	// the automation path lets the first reconciler tick bootstrap the cert,
	// exactly like the `jabali domain create` CLI. GUI create leaves it false.
	SkipInlineSSL bool
	// WebDisabled / DNSDisabled (GH #1449) opt the domain OUT of a service.
	// INVERTED on purpose: the zero value (false) means the service is ON, so
	// every existing caller (GUI create, automation) keeps creating a
	// full-service web+dns domain without changing a line. WebDisabled=true →
	// docroot-less (no vhost/PHP/web-SSL): a DNS-only zone or a mail-only
	// domain. DNSDisabled=true → the panel does not host DNS (external DNS).
	WebDisabled bool
	DNSDisabled bool
	// DNSApexIPv4 (GH #1540) is the apex IP for a DNS-only zone — the "pointed
	// IP" of the Add DNS Zone flow. Only meaningful when WebDisabled=true and
	// DNS is on: the panel seeds a single "@ A <ip>" row at zone bootstrap and
	// leaves it tenant-editable. Empty for every web/mail create (the apex is
	// panel-managed). Validated by domainops.Create (must be a bare IPv4, web
	// off, DNS on).
	DNSApexIPv4 string
	// DNSApexIPv6 (GH #1540 follow-up) is the optional apex IPv6 (AAAA) for a
	// DNS-only zone. Same web-off/DNS-on gate as DNSApexIPv4; must be a bare IPv6
	// (not an IPv4 or IPv4-mapped address). Empty when the zone has no v6 apex.
	DNSApexIPv6 string
}

// createDomainError carries the exact HTTP shape the inline create() used, so
// the extraction is byte-identical on the wire. Detail is optional.
type createDomainError struct {
	Status int
	Code   string
	Detail string
}

func (e *createDomainError) Error() string { return e.Code }

// reverseProxyReserveError maps a domainops reverse-proxy reservation failure
// to the status, code, and detail the create door returned before the module
// owned the reservation (JAB-279 AC6).
func reverseProxyReserveError(err error) *createDomainError {
	var invalid *domainops.InvalidPortError
	switch {
	case errors.Is(err, domainops.ErrReverseProxyUnavailable):
		return &createDomainError{http.StatusServiceUnavailable, "reverse_proxy_unavailable", "reverse-proxy domains are not enabled on this host"}
	case errors.As(err, &invalid):
		return &createDomainError{http.StatusBadRequest, "reverse_proxy_port_invalid", invalid.Reason.Error()}
	case errors.Is(err, domainops.ErrReverseProxyPortSystemBound):
		return &createDomainError{http.StatusConflict, "reverse_proxy_port_system_bound", "that port is already in use by a system service — choose another"}
	case errors.Is(err, domainops.ErrReverseProxyPortInUse):
		return &createDomainError{http.StatusConflict, "reverse_proxy_port_in_use", "that port is already assigned to another domain — choose another"}
	default:
		return &createDomainError{http.StatusServiceUnavailable, "reverse_proxy_port_unavailable", "no free reverse-proxy port available; contact the administrator"}
	}
}

// domainPostureError maps a domainops service-matrix or mail-posture rejection
// to the status, code, and detail the create door returned before the module
// owned the rules (JAB-279).
func domainPostureError(err error) *createDomainError {
	switch {
	case errors.Is(err, domainops.ErrWebOffReverseProxy):
		return &createDomainError{http.StatusBadRequest, "web_disabled_no_reverse_proxy", "a reverse-proxy domain requires web hosting"}
	case errors.Is(err, domainops.ErrWebOffTempURL):
		return &createDomainError{http.StatusBadRequest, "web_disabled_no_temp_url", "a preview URL requires web hosting"}
	case errors.Is(err, domainops.ErrWebOffDocRoot):
		return &createDomainError{http.StatusBadRequest, "web_disabled_no_docroot", "a web-disabled domain has no document root"}
	case errors.Is(err, domainops.ErrMailProviderInvalid):
		return &createDomainError{http.StatusBadRequest, "invalid_mail_provider", ""}
	case errors.Is(err, domainops.ErrMailProviderCustomReserved):
		return &createDomainError{http.StatusBadRequest, "mail_provider_custom_reserved", "select a DNS template via dns_template_id rather than setting mail_provider=custom directly"}
	case errors.Is(err, domainops.ErrDNSTemplatesUnavailable):
		return &createDomainError{http.StatusServiceUnavailable, "dns_templates_unavailable", "DNS templates are not enabled on this server"}
	case errors.Is(err, domainops.ErrDNSTemplateProviderExclusive):
		return &createDomainError{http.StatusBadRequest, "template_and_provider_exclusive", "a DNS template sets the mail posture; do not also select a mail provider"}
	case errors.Is(err, domainops.ErrDNSTemplateRequiresDNS):
		return &createDomainError{http.StatusBadRequest, "template_requires_dns", "a DNS template seeds records into the panel-hosted zone; this domain has DNS hosted externally"}
	case errors.Is(err, domainops.ErrDNSTemplateUnknown):
		return &createDomainError{http.StatusBadRequest, "unknown_dns_template", "the selected DNS template does not exist"}
	case errors.Is(err, domainops.ErrDNSTemplateLookup):
		return &createDomainError{http.StatusInternalServerError, "dns_template_lookup_failed", ""}
	case errors.Is(err, domainops.ErrSSLModeInvalid):
		return &createDomainError{http.StatusBadRequest, "invalid_ssl_mode", ""}
	case errors.Is(err, domainops.ErrSSLModeCustomAtCreate):
		return &createDomainError{http.StatusBadRequest, "ssl_mode_custom_requires_upload", "create the domain with le/self/none, then upload a custom cert via the SSL settings"}
	case errors.Is(err, domainops.ErrSSLModeSharedAtCreate):
		return &createDomainError{http.StatusBadRequest, "ssl_mode_shared_requires_attach", "create the domain with le/self/none, then attach a shared cert via POST /domains/:id/ssl/shared"}
	case errors.Is(err, domainops.ErrSSLNoneWithMail):
		return &createDomainError{http.StatusBadRequest, "ssl_none_with_email", "a mail-enabled domain needs TLS; choose le/self or set mail provider to none"}
	case errors.Is(err, domainops.ErrNoServiceSelected):
		return &createDomainError{http.StatusBadRequest, "no_service_selected", "select at least one service: web hosting, DNS, or mail"}
	default:
		return &createDomainError{http.StatusInternalServerError, "internal", ""}
	}
}

// createDomainOpError maps a domainops.Create rejection to the status, code,
// and detail the create door returned before the module owned the
// orchestration (JAB-279 AC1). Anything unrecognised is an opaque 500.
func createDomainOpError(err error) *createDomainError {
	var (
		aliasHit *domainops.AliasConflictError
		docRoot  *domainops.DocRootError
		tmpl     *domainops.WebTemplateInvalidError
		slug     *domainops.PreviewSlugConflictError
	)
	switch {
	case isDomainNameError(err):
		return &createDomainError{http.StatusBadRequest, "invalid_domain_name", domainNameError(err).Error()}
	case errors.Is(err, domainops.ErrAliasLookup):
		return &createDomainError{http.StatusInternalServerError, "db_alias_lookup", "could not verify the domain name against existing aliases"}
	case errors.As(err, &aliasHit):
		return &createDomainError{http.StatusConflict, "domain_conflicts_alias", aliasHit.Error()}
	case errors.Is(err, domainops.ErrOwnerRequired):
		return &createDomainError{http.StatusBadRequest, "user_id is required", ""}
	case errors.Is(err, domainops.ErrSuffixLookup):
		return &createDomainError{http.StatusInternalServerError, "db_suffix_lookup", "could not verify the domain name against existing domains"}
	case errors.Is(err, domainops.ErrDomainConflictsTenant):
		// Deliberately generic detail: naming the conflicting domain would leak
		// another tenant's zone/subdomain existence (GH #1789 child direction).
		return &createDomainError{http.StatusConflict, "domain_conflicts_tenant", "the name conflicts with a domain owned by another account"}
	case errors.Is(err, domainops.ErrApexIPWithWeb):
		return &createDomainError{http.StatusBadRequest, "web_enabled_apex_ip", "a web domain's apex IP is managed by the panel — set an apex IP only on a DNS-only zone"}
	case errors.Is(err, domainops.ErrApexIPWithoutDNS):
		return &createDomainError{http.StatusBadRequest, "dns_disabled_apex_ip", "an apex IP requires the panel to host DNS for this domain"}
	case errors.Is(err, domainops.ErrApexIPv4Invalid):
		return &createDomainError{http.StatusBadRequest, "invalid_apex_ip", "apex IP must be a valid IPv4 address"}
	case errors.Is(err, domainops.ErrApexIPv6Invalid):
		return &createDomainError{http.StatusBadRequest, "invalid_apex_ipv6", "apex IPv6 must be a valid IPv6 address"}
	case errors.Is(err, domainops.ErrOwnerLookup):
		if isNotFound(err) {
			return &createDomainError{http.StatusBadRequest, "user not found", ""}
		}
		return &createDomainError{http.StatusInternalServerError, "internal", ""}
	case errors.Is(err, domainops.ErrAdminCannotHost):
		return &createDomainError{http.StatusBadRequest, "admin_cannot_host", "admin users are panel-only — create a regular user to host domains"}
	case errors.Is(err, domainops.ErrOwnerSuspended):
		return &createDomainError{http.StatusConflict, "user_suspended", "user is suspended — unsuspend before adding domains"}
	case errors.Is(err, domainops.ErrDomainQuotaExceeded):
		return &createDomainError{http.StatusConflict, "domain_quota_exceeded", ""}
	case errors.As(err, &docRoot):
		return &createDomainError{http.StatusBadRequest, "invalid_document_root", docRootError(docRoot.Reason, docRoot.Username, docRoot.DomainName).Error()}
	case errors.Is(err, domainops.ErrWebTemplateAdminOnly):
		return &createDomainError{http.StatusForbidden, "web_template_admin_only", "web templates can be applied only by an administrator"}
	case errors.Is(err, domainops.ErrWebTemplatesUnavailable):
		return &createDomainError{http.StatusServiceUnavailable, "web_templates_unavailable", "web templates are not enabled on this server"}
	case errors.Is(err, domainops.ErrWebTemplateUnknown):
		return &createDomainError{http.StatusBadRequest, "unknown_web_template", "the selected web template does not exist"}
	case errors.Is(err, domainops.ErrWebTemplateLookup):
		return &createDomainError{http.StatusInternalServerError, "web_template_lookup_failed", ""}
	case errors.As(err, &tmpl):
		return &createDomainError{http.StatusBadRequest, "web_template_invalid", tmpl.Error()}
	case errors.Is(err, domainops.ErrM365OnmicrosoftInvalid):
		return &createDomainError{http.StatusBadRequest, "invalid_m365_onmicrosoft", err.Error()}
	case errors.Is(err, domainops.ErrGoogleDKIMInvalid):
		return &createDomainError{http.StatusBadRequest, "invalid_google_dkim", err.Error()}
	case errors.Is(err, domainops.ErrReverseProxyUnavailable),
		errors.Is(err, domainops.ErrReverseProxyPortInvalid),
		errors.Is(err, domainops.ErrReverseProxyPortSystemBound),
		errors.Is(err, domainops.ErrReverseProxyPortInUse),
		errors.Is(err, domainops.ErrReverseProxyPortUnavailable):
		return reverseProxyReserveError(err)
	case errors.As(err, &slug):
		return &createDomainError{http.StatusConflict, "temp_url_slug_conflict", slug.Error()}
	case errors.Is(err, domainops.ErrDomainExists):
		return &createDomainError{http.StatusConflict, "domain_already_exists", ""}
	default:
		// Web-off options, mail posture and the service matrix; anything else
		// (a store error, a wiring bug) falls through to the opaque 500.
		return domainPostureError(err)
	}
}

// createDomainOp runs domainops.Create for `in` against the handler's deps. On
// success it returns the created domain (with any in-place update from the
// shared-cert attach). On failure it returns a createDomainError the caller
// renders verbatim.
func createDomainOp(ctx context.Context, h *domainHandler, in createDomainInput) (*models.Domain, *createDomainError) {
	res, err := domainops.Create(ctx, domainops.CreateDeps{
		Domains:               h.cfg.Domains,
		Users:                 h.cfg.Users,
		Aliases:               h.cfg.WebDomainAliases,
		Packages:              h.cfg.Packages,
		Settings:              h.cfg.ServerSettings,
		DNSTemplates:          h.cfg.DNSTemplates,
		WebTemplates:          h.cfg.WebTemplates,
		ValidateWebDirectives: ValidateNginxDirectivesAdmin,
		SharedCerts:           h.cfg.SharedCerts,
		Ports:                 h.cfg.PortAllocations,
		Agent:                 h.cfg.Agent,
	}, h.createDomainHooks(in.SkipInlineSSL), domainops.CreateInput{
		OwnerID:          in.OwnerID,
		Name:             in.Name,
		DocRoot:          in.DocRoot,
		ActorIsAdmin:     in.ActorIsAdmin,
		MailProvider:     in.MailProvider,
		M365Onmicrosoft:  in.M365Onmicrosoft,
		GoogleDKIM:       in.GoogleDKIM,
		DNSTemplateID:    in.DNSTemplateID,
		WebTemplateID:    in.WebTemplateID,
		SSLMode:          in.SSLMode,
		CreateWWW:        in.CreateWWW,
		TempURLEnabled:   in.TempURLEnabled,
		ReverseProxy:     in.ReverseProxy,
		ReverseProxyPort: int(in.ReverseProxyPort),
		WebDisabled:      in.WebDisabled,
		DNSDisabled:      in.DNSDisabled,
		DNSApexIPv4:      in.DNSApexIPv4,
		DNSApexIPv6:      in.DNSApexIPv6,
	})
	if err != nil {
		return nil, createDomainOpError(err)
	}

	// The fast paths are soft: the create has succeeded, and the reconciler
	// issues or attaches a certificate, and finishes a mail enable, on its next
	// tick. They are logged, never returned in the response.
	d := res.Domain
	if res.SharedCertErr != nil {
		slog.Warn("shared-certificate auto-attach skipped during domain.create (the reconciler retries)",
			"domain_id", d.ID, "domain", d.Name, "err", res.SharedCertErr)
	}
	if res.MailErr != nil {
		slog.Warn("auto-enable email failed during domain.create (the reconciler retries; operator can also retry from UI)",
			"domain_id", d.ID, "domain", d.Name, "err", res.MailErr)
	} else if len(res.MailWarnings) > 0 {
		slog.Info("auto-enable email DNS autoconfig warnings",
			"domain_id", d.ID, "domain", d.Name, "warnings", res.MailWarnings)
	}
	return d, nil
}

// createDomainHooks are the post-create fast paths this process can run. The
// reconcile schedule and the inline SSL attempt need the in-process
// reconciler; skipInlineSSL leaves the cert to the first reconciler tick
// (JAB-233 automation). The mail enable (ADR-0013) needs the agent and the DNS
// stores.
func (h *domainHandler) createDomainHooks(skipInlineSSL bool) domainops.CreateHooks {
	var hooks domainops.CreateHooks
	if rec := h.cfg.Reconciler; rec != nil {
		hooks.Schedule = rec.Schedule
		if !skipInlineSSL {
			// Inline SSL (30s): ACME with self-signed fallback. Never errors —
			// cert state is already in the DB.
			hooks.InlineSSL = func(ctx context.Context, d *models.Domain) {
				inlineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				rec.ReconcileSSLInline(inlineCtx, d)
			}
		}
	}
	if h.cfg.Agent != nil && h.cfg.DNSZones != nil && h.cfg.DNSRecords != nil {
		hooks.EnableMail = func(ctx context.Context, d *models.Domain) ([]string, error) {
			_, _, warnings, err := domainmailops.Enable(ctx, domainmailops.Deps{
				Call:           agentCall(h.cfg.Agent),
				Domains:        h.cfg.Domains,
				DNSZones:       h.cfg.DNSZones,
				DNSRecords:     h.cfg.DNSRecords,
				ServerSettings: h.cfg.ServerSettings,
				SSLCerts:       h.cfg.SSLCerts,
				SSLReconciler:  h.cfg.Reconciler,
			}, d)
			if err != nil {
				return nil, err
			}
			return domainmailops.WarningMessages(warnings), nil
		}
	}
	return hooks
}
