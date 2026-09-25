package domainops

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Create is the domain lifecycle module's one create entrypoint (JAB-279 AC1).
// It runs every rule a new domain must pass, in one order, then stores the row
// and runs the post-create fast paths. The REST create door, the billing
// automation and `jabali domain create` are adapters: each authenticates its
// caller, reads its own transport into a CreateInput, supplies the hooks it
// can run, and maps the typed errors below to its own status codes or
// messages (ADR-0083: auth stays adapter-side).
//
// The order is the REST door's historical order, so the first rejection a
// request with several faults gets is unchanged there:
//
//	name → alias collision → owner id → cross-tenant suffix (non-admin) →
//	web-off options → apex IPs → owner lookup → owner eligibility → quota →
//	document root → mail posture → web template → mail-provider tokens →
//	service matrix → reverse-proxy port → preview slug → insert →
//	shared-cert attach → inline SSL → mail enable → reconcile schedule
//
// Every step up to the insert is a hard rejection; nothing is stored when one
// fails, and a reserved port is released. Every step after the insert is a
// fast path: the create has succeeded, and a failure is reported in the
// result for the adapter to surface. The reconciler converges anything a fast
// path did not finish.

// Create rejections that have no older sentinel. Each adapter maps them.
var (
	// ErrCreateDeps means a required Create dependency is not wired — a
	// wiring bug, not a policy result.
	ErrCreateDeps = errors.New("domainops: create dependencies are not wired")
	// ErrOwnerRequired means CreateInput.OwnerID was empty.
	ErrOwnerRequired = errors.New("domainops: owner id is required")
	// ErrOwnerLookup wraps the owner store's error. errors.Is also matches the
	// store error, so an adapter can tell repository.ErrNotFound apart.
	ErrOwnerLookup = errors.New("domainops: owner lookup failed")
	// ErrDomainConflictsAlias is matched by *AliasConflictError.
	ErrDomainConflictsAlias = errors.New("domainops: domain name is an alias of another domain")
	// ErrAliasLookup wraps an alias-table lookup error (the guard fails closed).
	ErrAliasLookup = errors.New("domainops: alias lookup failed")
	// ErrDomainConflictsTenant means the name nests under, or wraps, a domain
	// another tenant owns (GH #1789). The conflicting name is deliberately not
	// carried: naming it would leak another tenant's zone.
	ErrDomainConflictsTenant = errors.New("domainops: domain name conflicts with another tenant's domain")
	// ErrSuffixLookup wraps a domain-table lookup error (the guard fails closed).
	ErrSuffixLookup = errors.New("domainops: cross-tenant lookup failed")
	// ErrApexIPWithWeb means an apex IP was given for a web domain, whose apex
	// the panel manages (GH #1540).
	ErrApexIPWithWeb = errors.New("domainops: apex IP set on a web domain")
	// ErrApexIPWithoutDNS means an apex IP was given for a domain whose DNS is
	// hosted elsewhere.
	ErrApexIPWithoutDNS = errors.New("domainops: apex IP set without panel DNS")
	// ErrApexIPv4Invalid means the apex IPv4 is not a bare IPv4 address.
	ErrApexIPv4Invalid = errors.New("domainops: apex IPv4 is invalid")
	// ErrApexIPv6Invalid means the apex IPv6 is not a bare IPv6 address.
	ErrApexIPv6Invalid = errors.New("domainops: apex IPv6 is invalid")
	// ErrWebTemplateAdminOnly means a non-admin actor named a web template
	// (GH #1624 / ADR-0169 Phase 3).
	ErrWebTemplateAdminOnly = errors.New("domainops: web templates are admin-only")
	// ErrWebTemplatesUnavailable means a web template was named on a host with
	// no web-template store. Fail closed: never a silent skip.
	ErrWebTemplatesUnavailable = errors.New("domainops: web templates are not enabled")
	// ErrWebTemplateUnknown means the named web template does not exist.
	ErrWebTemplateUnknown = errors.New("domainops: web template does not exist")
	// ErrWebTemplateLookup wraps a web-template store error.
	ErrWebTemplateLookup = errors.New("domainops: web template lookup failed")
	// ErrWebTemplateInvalid is matched by *WebTemplateInvalidError.
	ErrWebTemplateInvalid = errors.New("domainops: web template directives are invalid")
	// ErrM365OnmicrosoftInvalid wraps the M365 tenant-name validator's error.
	ErrM365OnmicrosoftInvalid = errors.New("domainops: M365 onmicrosoft name is invalid")
	// ErrGoogleDKIMInvalid wraps the Google DKIM validator's error.
	ErrGoogleDKIMInvalid = errors.New("domainops: Google DKIM value is invalid")
	// ErrPreviewSlugConflict is matched by *PreviewSlugConflictError.
	ErrPreviewSlugConflict = errors.New("domainops: preview URL slug collides")
)

// AliasConflictError names the helper server_name another domain's alias
// already holds.
type AliasConflictError struct{ Hostname string }

func (e *AliasConflictError) Error() string {
	return "the name " + e.Hostname + " is already used as an alias of another domain"
}
func (e *AliasConflictError) Unwrap() error { return ErrDomainConflictsAlias }

// DocRootError carries the owner and domain a rejected document root was
// checked against, so an adapter can rebuild its message. Unwrap yields the
// docroot sentinel (ErrDocRootOutsideHome, ErrDocRootTraversal or
// ErrDocRootOutsideDomain).
type DocRootError struct {
	Reason     error
	Username   string
	DomainName string
}

func (e *DocRootError) Error() string { return e.Reason.Error() }
func (e *DocRootError) Unwrap() error { return e.Reason }

// WebTemplateInvalidError is a web template whose stored directives no longer
// pass the admin validator (a denylist tightened after the template was saved
// is enforced at apply).
type WebTemplateInvalidError struct {
	Name   string
	Reason string
}

func (e *WebTemplateInvalidError) Error() string {
	return "web template " + e.Name + ": " + e.Reason
}
func (e *WebTemplateInvalidError) Unwrap() error { return ErrWebTemplateInvalid }

// PreviewSlugConflictError names the domain whose preview slug the new domain
// would share (dots→dashes is not injective).
type PreviewSlugConflictError struct{ Other string }

func (e *PreviewSlugConflictError) Error() string {
	return "preview URL would collide with " + e.Other
}
func (e *PreviewSlugConflictError) Unwrap() error { return ErrPreviewSlugConflict }

// CreateInput is one create request after the adapter has authenticated the
// caller. Name must already be normalized (NormalizeDomainName); Create
// validates it but does not rewrite it.
type CreateInput struct {
	OwnerID string
	Name    string
	// DocRoot "" derives /home/<user>/domains/<name>/public_html.
	DocRoot string
	// ActorIsAdmin is the CALLER's privilege, not the owner's. An admin actor
	// skips the cross-tenant suffix guard (trusted delegation), may place the
	// document root anywhere under the owner's home, and may apply a web
	// template. The zero value is the strict, fail-closed tenant rule.
	ActorIsAdmin    bool
	MailProvider    string // "" → jabali
	M365Onmicrosoft string
	GoogleDKIM      string
	// DNSTemplateID seeds the fresh zone from a custom DNS template (GH #1627)
	// and sets the mail posture to external.
	DNSTemplateID string
	// WebTemplateID snapshot-copies an admin web template's nginx directives
	// onto the new domain (GH #1624). Admin-only.
	WebTemplateID  string
	SSLMode        string // "" → le
	CreateWWW      bool
	TempURLEnabled bool
	// ReverseProxy allocates a loopback port the vhost proxies to (GH #1175);
	// ReverseProxyPort 0 auto-assigns, any other value is the chosen port
	// (GH #1401).
	ReverseProxy     bool
	ReverseProxyPort int
	// WebDisabled / DNSDisabled opt the domain out of a service (GH #1449).
	// Inverted, so the zero value is a full-service domain.
	WebDisabled bool
	DNSDisabled bool
	// DNSApexIPv4 / DNSApexIPv6 are the apex addresses of a DNS-only zone
	// (GH #1540). Only valid when web is off and DNS is on.
	DNSApexIPv4 string
	DNSApexIPv6 string
}

// OwnerFinder resolves CreateInput.OwnerID to the owning user.
type OwnerFinder interface {
	FindByID(ctx context.Context, id string) (*models.User, error)
}

// WebTemplateFinder reads an admin web template.
type WebTemplateFinder interface {
	FindByID(ctx context.Context, id string) (*models.WebTemplate, error)
}

// CreateDomainStore is the slice of the domain repository Create needs.
type CreateDomainStore interface {
	SuffixDomainFinder
	DomainCounter
	PreviewDomainLister
	DomainCreator
	SharedCertSetter
}

// CreateDeps are Create's collaborators. Domains and Users are required. A nil
// optional store means that feature is unwired on this host: no alias table
// (no alias collision), no DNS templates, no web templates (a named web
// template is then rejected), no shared certificates (no attach), no port
// allocator (reverse-proxy domains are rejected). Packages is required for an
// owner on a package.
type CreateDeps struct {
	Domains      CreateDomainStore
	Users        OwnerFinder
	Aliases      AliasHostnameFinder
	Packages     PackageFinder
	Settings     MailSettingsReader
	DNSTemplates DNSTemplateFinder
	WebTemplates WebTemplateFinder
	// ValidateWebDirectives is the admin nginx-directive validator; it returns
	// "" when the directives are acceptable, otherwise the reason. Required
	// whenever WebTemplates is set.
	ValidateWebDirectives func(directives string) string
	SharedCerts           SharedCertLister
	Ports                 repository.PortAllocationRepository
	// Agent answers the reverse-proxy port probe. Leave it nil when the
	// caller's client pointer is nil (a nil *agent.Client in the interface is
	// not a nil interface).
	Agent agent.AgentInterface
	// Log records the checks Create skips fail-open. Nil uses slog.Default().
	Log *slog.Logger
}

// CreateHooks are the post-create fast paths an adapter can run. A nil hook
// is skipped; the reconciler converges what it would have done.
type CreateHooks struct {
	// Schedule queues a reconcile of the new domain. Nil when the caller holds
	// no in-process reconciler (the CLI): the next tick picks the row up.
	Schedule func(domainID string)
	// InlineSSL makes one bounded attempt to issue the web certificate before
	// Create returns. It runs only for a web domain no shared certificate was
	// attached to. Nil lets the first reconciler tick bootstrap the cert.
	InlineSSL func(ctx context.Context, d *models.Domain)
	// EnableMail registers a Jabali-mail domain on the mail server and returns
	// soft DNS warnings. It runs only when the mail provider is jabali. A
	// failure does not fail the create: the row keeps email_enabled=1 and the
	// reconciler finishes the enable on its next tick.
	EnableMail func(ctx context.Context, d *models.Domain) (warnings []string, err error)
}

// CreateResult is a stored domain and what its fast paths reported.
type CreateResult struct {
	// Domain is the stored row, updated in place by the shared-cert attach.
	Domain *models.Domain
	// SharedCert is the covering shared certificate: attached when
	// SharedCertErr is nil, or the one that failed to attach.
	SharedCert *models.SharedCertificate
	// SharedCertErr is ErrSharedCertLookup or ErrSharedCertAttach. Soft.
	SharedCertErr error
	// MailWarnings and MailErr are what EnableMail returned. Soft.
	MailWarnings []string
	MailErr      error
}

// Create validates in, stores the domain, and runs the post-create hooks. See
// the file comment for the order. On a rejection nothing is stored.
func Create(ctx context.Context, d CreateDeps, hooks CreateHooks, in CreateInput) (*CreateResult, error) {
	if d.Domains == nil || d.Users == nil {
		return nil, ErrCreateDeps
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}

	if err := ValidateDomainName(in.Name); err != nil {
		return nil, err
	}

	// GH #1625: the name, www.<name> and the mail-helper server_names must not
	// be held by another domain's web alias. Fail closed on a lookup error.
	if hit, clash, err := AliasCollision(ctx, d.Aliases, in.Name); err != nil {
		return nil, &kindError{kind: ErrAliasLookup, cause: err}
	} else if clash {
		return nil, &AliasConflictError{Hostname: hit}
	}

	if in.OwnerID == "" {
		return nil, ErrOwnerRequired
	}

	// GH #1789: a tenant must not nest under, or wrap, another tenant's
	// domain. An admin actor is trusted to place cross-tenant delegations.
	// Fail closed on a lookup error.
	if !in.ActorIsAdmin {
		if _, clash, err := CrossTenantSuffixCollision(ctx, d.Domains, in.Name, in.OwnerID); err != nil {
			return nil, &kindError{kind: ErrSuffixLookup, cause: err}
		} else if clash {
			return nil, ErrDomainConflictsTenant
		}
	}

	webEnabled := !in.WebDisabled
	dnsEnabled := !in.DNSDisabled
	if err := CheckWebOffOptions(WebOffInput{
		WebEnabled:   webEnabled,
		ReverseProxy: in.ReverseProxy,
		TempURL:      in.TempURLEnabled,
		DocRoot:      in.DocRoot,
	}); err != nil {
		return nil, err
	}

	apexIPv4, apexIPv6, err := resolveApexIPs(in, webEnabled, dnsEnabled)
	if err != nil {
		return nil, err
	}

	owner, err := d.Users.FindByID(ctx, in.OwnerID)
	if err != nil {
		return nil, &kindError{kind: ErrOwnerLookup, cause: err}
	}
	if err := CheckOwnerEligible(owner); err != nil {
		return nil, err
	}
	if err := CheckDomainQuota(ctx, QuotaDeps{Domains: d.Domains, Packages: d.Packages}, owner); err != nil {
		return nil, err
	}

	// CheckOwnerEligible rejected a nil or empty username above.
	docRoot, err := resolveDocRoot(in, *owner.Username, webEnabled)
	if err != nil {
		return nil, err
	}

	posture, err := ResolveMailPosture(ctx, d.DNSTemplates, MailPostureInput{
		Provider:          in.MailProvider,
		DNSTemplateID:     in.DNSTemplateID,
		DNSEnabled:        dnsEnabled,
		MailModuleEnabled: MailModuleEnabled(ctx, d.Settings),
	})
	if err != nil {
		return nil, err
	}

	webTemplateID, webDirectives, err := resolveWebTemplate(ctx, d, in)
	if err != nil {
		return nil, err
	}

	m365Tenant, err := dnscompile.NormaliseM365Onmicrosoft(in.M365Onmicrosoft)
	if err != nil {
		return nil, &kindError{kind: ErrM365OnmicrosoftInvalid, cause: err}
	}
	googleDKIM, err := dnscompile.ValidateGoogleDKIM(in.GoogleDKIM)
	if err != nil {
		return nil, &kindError{kind: ErrGoogleDKIMInvalid, cause: err}
	}

	matrix, err := ResolveServiceMatrix(ServiceMatrixInput{
		WebEnabled:   webEnabled,
		DNSEnabled:   dnsEnabled,
		MailProvider: posture.Provider,
		SSLMode:      in.SSLMode,
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	dom := &models.Domain{
		ID:                    ids.NewULID(),
		UserID:                in.OwnerID,
		Name:                  in.Name,
		DocRoot:               docRoot,
		IsEnabled:             true,
		SSLMode:               matrix.SSLMode,
		SSLEnabled:            models.SSLEnabledForMode(matrix.SSLMode),
		MailProvider:          posture.Provider,
		M365Onmicrosoft:       ptrOrNil(m365Tenant),
		GoogleDKIM:            ptrOrNil(googleDKIM),
		MailTemplateID:        posture.TemplateID,
		WebTemplateID:         webTemplateID,
		NginxCustomDirectives: ptrOrNil(webDirectives),
		EmailEnabled:          matrix.EmailEnabled,
		SkipAutoSAN:           matrix.SkipAutoSAN,
		CreateWWW:             in.CreateWWW,
		TempURLEnabled:        in.TempURLEnabled,
		WebDisabled:           in.WebDisabled,
		DNSDisabled:           in.DNSDisabled,
		DNSApexIPv4:           ptrOrNil(apexIPv4),
		DNSApexIPv6:           ptrOrNil(apexIPv6),
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	// The port is reserved BEFORE the insert (owner_id = the new ULID) so the
	// row carries it. Every rejection after this point releases it (AC4).
	if in.ReverseProxy {
		port, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: d.Ports, Agent: d.Agent}, dom.ID, in.ReverseProxyPort)
		if err != nil {
			return nil, err
		}
		dom.ReverseProxyPort = uint32(port)
	}

	// Refuse the second enable of a colliding preview slug: nginx first-wins
	// would silently serve the wrong site. A store error fails open (nginx
	// first-wins is the backstop), but the skip is logged.
	if dom.TempURLEnabled {
		other, err := PreviewSlugConflict(ctx, d.Domains, dom.Name, "")
		if err != nil {
			log.Warn("preview-slug collision check skipped (nginx first-wins is the backstop)",
				"domain", dom.Name, "err", err)
		} else if other != "" {
			if in.ReverseProxy {
				_ = ReleaseReverseProxyPort(ctx, d.Ports, dom.ID)
			}
			return nil, &PreviewSlugConflictError{Other: other}
		}
	}

	// PersistDomain releases the reverse-proxy port on any insert failure.
	if err := PersistDomain(ctx, d.Domains, d.Ports, dom); err != nil {
		return nil, err
	}

	return runCreateHooks(ctx, d, hooks, dom, webEnabled), nil
}

// runCreateHooks runs the post-insert fast paths. None of them can fail the
// create.
func runCreateHooks(ctx context.Context, d CreateDeps, hooks CreateHooks, dom *models.Domain, webEnabled bool) *CreateResult {
	res := &CreateResult{Domain: dom}

	// A covering shared certificate gives HTTPS at once, with no ACME wait
	// (JAB-170 phase 5). An explicit self/none mode is kept, and a web-off
	// domain has no web certificate (AttachCoveringSharedCert skips both).
	res.SharedCert, res.SharedCertErr = AttachCoveringSharedCert(ctx, SharedCertDeps{
		Certs:   d.SharedCerts,
		Domains: d.Domains,
	}, dom)
	attached := res.SharedCertErr == nil && res.SharedCert != nil
	if attached && hooks.Schedule != nil {
		hooks.Schedule(dom.ID)
	}

	if webEnabled && !attached && hooks.InlineSSL != nil {
		hooks.InlineSSL(ctx, dom)
	}

	if dom.MailProvider == models.MailProviderJabali && hooks.EnableMail != nil {
		res.MailWarnings, res.MailErr = hooks.EnableMail(ctx, dom)
	}

	// Converge OS-level state (vhost, PHP pool, …) with the new row.
	if hooks.Schedule != nil {
		hooks.Schedule(dom.ID)
	}
	return res
}

// resolveApexIPs validates the DNS-only zone apex addresses (GH #1540). They
// are only meaningful for a web-off zone whose DNS the panel hosts: a web
// domain's apex is panel-managed, and an external-DNS domain publishes nothing
// here. Each is parsed as a BARE address of its own family and returned in
// canonical form, so a zone stores one row shape per address.
func resolveApexIPs(in CreateInput, webEnabled, dnsEnabled bool) (v4, v6 string, err error) {
	raw4 := strings.TrimSpace(in.DNSApexIPv4)
	raw6 := strings.TrimSpace(in.DNSApexIPv6)
	if raw4 == "" && raw6 == "" {
		return "", "", nil
	}
	if webEnabled {
		return "", "", ErrApexIPWithWeb
	}
	if !dnsEnabled {
		return "", "", ErrApexIPWithoutDNS
	}
	if raw4 != "" {
		ip := net.ParseIP(raw4)
		if ip == nil || ip.To4() == nil {
			return "", "", ErrApexIPv4Invalid
		}
		v4 = ip.String()
	}
	if raw6 != "" {
		ip := net.ParseIP(raw6)
		// To4() != nil means it parsed as IPv4 (or IPv4-mapped) — not a bare
		// IPv6, so it is rejected for the AAAA field.
		if ip == nil || ip.To4() != nil {
			return "", "", ErrApexIPv6Invalid
		}
		v6 = ip.String()
	}
	return v4, v6, nil
}

// resolveDocRoot trims and confines the document root, or derives the default.
// A web-off domain has none (CheckWebOffOptions already rejected a non-empty
// one). An admin actor may use anywhere under the owner's home; a tenant is
// held to the domain's own tree (GH #526 / GH #1413).
func resolveDocRoot(in CreateInput, username string, webEnabled bool) (string, error) {
	if !webEnabled {
		return "", nil
	}
	docRoot := strings.TrimSpace(in.DocRoot)
	validate := ValidateTenantDocumentRoot
	if in.ActorIsAdmin {
		validate = ValidateDocumentRoot
	}
	if err := validate(docRoot, username, in.Name); err != nil {
		return "", &DocRootError{Reason: err, Username: username, DomainName: in.Name}
	}
	if docRoot == "" {
		docRoot = "/home/" + username + "/domains/" + in.Name + "/public_html"
	}
	return docRoot, nil
}

// resolveWebTemplate reads the admin web template a create names (GH #1624 /
// ADR-0169 Phase 3) and returns its id and directives for the snapshot copy.
//
// ADMIN-ONLY: the admin directive denylist does not block proxy_pass, so a
// tenant picking a globally-visible template could front a localhost service
// from their own domain (the ADR-0169 SSRF threat, with the admin as unwitting
// author). A missing store fails closed. The directives are validated AGAIN so
// a denylist tightened after the template was saved is enforced at apply.
func resolveWebTemplate(ctx context.Context, d CreateDeps, in CreateInput) (*string, string, error) {
	id := strings.TrimSpace(in.WebTemplateID)
	if id == "" {
		return nil, "", nil
	}
	if !in.ActorIsAdmin {
		return nil, "", ErrWebTemplateAdminOnly
	}
	if d.WebTemplates == nil || d.ValidateWebDirectives == nil {
		return nil, "", ErrWebTemplatesUnavailable
	}
	tmpl, err := d.WebTemplates.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, "", ErrWebTemplateUnknown
		}
		return nil, "", &kindError{kind: ErrWebTemplateLookup, cause: err}
	}
	if reason := d.ValidateWebDirectives(tmpl.NginxDirectives); reason != "" {
		return nil, "", &WebTemplateInvalidError{Name: tmpl.Name, Reason: reason}
	}
	return &id, tmpl.NginxDirectives, nil
}

func ptrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
