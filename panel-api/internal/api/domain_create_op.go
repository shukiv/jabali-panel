package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_create_op.go — JAB-233. The domain-creation orchestration extracted
// from domainHandler.create so a second caller (the billing automation
// userCreateHandler) can create a primary domain with the EXACT same GUI
// semantics. Zero-behavior-change: domainHandler.create is now a thin wrapper
// that binds+normalizes the request and maps createDomainError back to the
// same HTTP status/error/detail it emitted inline before.
//
// It stays in the api package (not a standalone domainops package) because it
// depends on a cluster of api-package helpers — validateDomainName,
// validateDocumentRoot, EnableDomainEmailInline, and the domainHandler methods
// findCoveringSharedCert / previewSlugConflict — and the only new caller
// (automation) also lives in this package. Reuse, not packaging, was the goal.

// createDomainInput is the pre-normalized, pre-validated-shape input to
// createDomainOp. Name MUST already be normalized (normalizeDomainName) and
// HTML-stripped by the caller — the op validates it but does not re-normalize,
// matching the create() source of truth.

type createDomainInput struct {
	OwnerID         string
	Name            string
	DocRoot         string // "" → derived under /home/<user>/domains/<name>/public_html
	MailProvider    string // "" → jabali
	M365Onmicrosoft string
	GoogleDKIM      string
	SSLMode         string // "" → le
	CreateWWW       bool
	TempURLEnabled  bool
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
}

// createDomainError carries the exact HTTP shape the inline create() used, so
// the extraction is byte-identical on the wire. Detail is optional.
type createDomainError struct {
	Status int
	Code   string
	Detail string
}

func (e *createDomainError) Error() string { return e.Code }

// createDomainOp runs the full GUI domain-creation orchestration for `in`
// against the handler's deps. On success it returns the created domain (with
// any in-place mutations from shared-cert attach / inline email). On failure it
// returns a createDomainError the caller renders verbatim.
func createDomainOp(ctx context.Context, h *domainHandler, in createDomainInput) (*models.Domain, *createDomainError) {
	// SECURITY: validate domain name (XSS / path traversal). Name is assumed
	// already normalized + HTML-stripped by the caller.
	if err := validateDomainName(in.Name); err != nil {
		return nil, &createDomainError{http.StatusBadRequest, "invalid_domain_name", err.Error()}
	}

	if in.OwnerID == "" {
		return nil, &createDomainError{http.StatusBadRequest, "user_id is required", ""}
	}

	user, err := h.cfg.Users.FindByID(ctx, in.OwnerID)
	if err != nil {
		if isNotFound(err) {
			return nil, &createDomainError{http.StatusBadRequest, "user not found", ""}
		}
		return nil, &createDomainError{http.StatusInternalServerError, "internal", ""}
	}

	// Admins are panel-only — no /home/<name>, so domains can't host under them.
	if user.IsAdmin {
		return nil, &createDomainError{http.StatusBadRequest, "admin_cannot_host", "admin users are panel-only — create a regular user to host domains"}
	}

	// Suspended users can't acquire new domains — a fresh vhost would be live
	// while the account stays locked, defeating the suspend cascade.
	if user.Suspended {
		return nil, &createDomainError{http.StatusConflict, "user_suspended", "user is suspended — unsuspend before adding domains"}
	}

	if user.Username == nil || *user.Username == "" {
		return nil, &createDomainError{http.StatusInternalServerError, "internal", ""}
	}

	// Quota check.
	if user.PackageID != nil && *user.PackageID != "" {
		count, err := h.cfg.Domains.CountByUserID(ctx, in.OwnerID)
		if err != nil {
			return nil, &createDomainError{http.StatusInternalServerError, "internal", ""}
		}
		pkg, err := h.cfg.Packages.FindByID(ctx, *user.PackageID)
		if err == nil && pkg.MaxDomains > 0 && count >= int64(pkg.MaxDomains) {
			return nil, &createDomainError{http.StatusConflict, "domain_quota_exceeded", ""}
		}
	}

	// SECURITY: validate custom document root path.
	if err := validateDocumentRoot(in.DocRoot, *user.Username, in.Name); err != nil {
		return nil, &createDomainError{http.StatusBadRequest, "invalid_document_root", err.Error()}
	}

	docRoot := in.DocRoot
	if docRoot == "" {
		docRoot = "/home/" + *user.Username + "/domains/" + in.Name + "/public_html"
	}

	// GH#181 mail provider: default jabali; EmailEnabled/SkipAutoSAN derived.
	mailProvider := in.MailProvider
	if mailProvider == "" {
		mailProvider = models.MailProviderJabali
	}
	if !models.ValidMailProvider(mailProvider) {
		return nil, &createDomainError{http.StatusBadRequest, "invalid_mail_provider", ""}
	}
	m365Tenant, err := dnscompile.NormaliseM365Onmicrosoft(in.M365Onmicrosoft)
	if err != nil {
		return nil, &createDomainError{http.StatusBadRequest, "invalid_m365_onmicrosoft", err.Error()}
	}
	googleDKIM, err := dnscompile.ValidateGoogleDKIM(in.GoogleDKIM)
	if err != nil {
		return nil, &createDomainError{http.StatusBadRequest, "invalid_google_dkim", err.Error()}
	}
	sslMode := in.SSLMode
	if sslMode == "" {
		sslMode = models.SSLModeLE
	}
	if !models.ValidSSLMode(sslMode) {
		return nil, &createDomainError{http.StatusBadRequest, "invalid_ssl_mode", ""}
	}
	if sslMode == models.SSLModeCustom {
		return nil, &createDomainError{http.StatusBadRequest, "ssl_mode_custom_requires_upload", "create the domain with le/self/none, then upload a custom cert via the SSL settings"}
	}
	if sslMode == models.SSLModeShared {
		return nil, &createDomainError{http.StatusBadRequest, "ssl_mode_shared_requires_attach", "create the domain with le/self/none, then attach a shared cert via POST /domains/:id/ssl/shared"}
	}

	mailEnabled, mailSkipSAN := models.DeriveMailFlags(mailProvider)
	// Email-enabled + no TLS is contradictory (MTA-STS / autoconfig need HTTPS).
	if sslMode == models.SSLModeNone && mailEnabled {
		return nil, &createDomainError{http.StatusBadRequest, "ssl_none_with_email", "a mail-enabled domain needs TLS; choose le/self or set mail provider to none"}
	}

	now := time.Now().UTC()
	domain := &models.Domain{
		ID:              ids.NewULID(),
		UserID:          in.OwnerID,
		Name:            in.Name,
		DocRoot:         docRoot,
		IsEnabled:       true,
		SSLMode:         sslMode,
		SSLEnabled:      models.SSLEnabledForMode(sslMode),
		MailProvider:    mailProvider,
		M365Onmicrosoft: strPtrOrNil(m365Tenant),
		GoogleDKIM:      strPtrOrNil(googleDKIM),
		EmailEnabled:    mailEnabled,
		SkipAutoSAN:     mailSkipSAN,
		CreateWWW:       in.CreateWWW,
		TempURLEnabled:  in.TempURLEnabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// GH #1175: a reverse-proxy domain draws a loopback port from the shared
	// allocator. Allocated BEFORE the insert (owner_id = the ULID above) so the
	// row carries the port; released if the insert then fails so we don't leak.
	if in.ReverseProxy {
		if h.cfg.PortAllocations == nil {
			return nil, &createDomainError{http.StatusServiceUnavailable, "reverse_proxy_unavailable", "reverse-proxy domains are not enabled on this host"}
		}
		var port int
		var aerr error
		if in.ReverseProxyPort != 0 {
			// GH #1401: tenant chose a specific port — validate it can't target
			// the panel, a system service, another tenant's allocated port, or a
			// privileged/reserved range, then reserve that exact port.
			if verr := repository.ValidateReverseProxyPort(int(in.ReverseProxyPort)); verr != nil {
				return nil, &createDomainError{http.StatusBadRequest, "reverse_proxy_port_invalid", verr.Error()}
			}
			port, aerr = h.cfg.PortAllocations.AllocateReverseProxySpecific(ctx, domain.ID, int(in.ReverseProxyPort))
			if errors.Is(aerr, repository.ErrPortInUse) {
				return nil, &createDomainError{http.StatusConflict, "reverse_proxy_port_in_use", "that port is already assigned to another domain — choose another"}
			}
		} else {
			port, aerr = h.cfg.PortAllocations.AllocateReverseProxy(ctx, domain.ID)
		}
		if aerr != nil {
			return nil, &createDomainError{http.StatusServiceUnavailable, "reverse_proxy_port_unavailable", "no free reverse-proxy port available; contact the administrator"}
		}
		domain.ReverseProxyPort = uint32(port)
	}

	// Preview-slug collision (dots→dashes is not injective): refuse the second
	// enable of a colliding pair — first-wins would silently serve the wrong site.
	if domain.TempURLEnabled {
		if other := h.previewSlugConflict(ctx, domain.Name, ""); other != "" {
			if in.ReverseProxy && h.cfg.PortAllocations != nil {
				_ = h.cfg.PortAllocations.Release(ctx, models.PortOwnerReverseProxy, domain.ID)
			}
			return nil, &createDomainError{http.StatusConflict, "temp_url_slug_conflict", "preview URL would collide with " + other}
		}
	}

	if err := h.cfg.Domains.Create(ctx, domain); err != nil {
		if in.ReverseProxy && h.cfg.PortAllocations != nil {
			_ = h.cfg.PortAllocations.Release(ctx, models.PortOwnerReverseProxy, domain.ID)
		}
		if isConflict(err) {
			return nil, &createDomainError{http.StatusConflict, "domain_already_exists", ""}
		}
		return nil, &createDomainError{http.StatusInternalServerError, "internal", ""}
	}

	// JAB-170 phase 5: auto-attach a covering shared cert (HTTPS instantly, no
	// ACME) and skip the inline ACME below.
	attachedShared := false
	if h.cfg.SharedCerts != nil {
		if cert := h.findCoveringSharedCert(ctx, domain.Name, domain.UserID); cert != nil {
			if err := h.cfg.Domains.SetSharedCertificate(ctx, domain.ID, &cert.ID, models.SSLModeShared); err == nil {
				domain.SSLMode = models.SSLModeShared
				domain.SharedCertificateID = &cert.ID
				attachedShared = true
				if h.cfg.Reconciler != nil {
					h.cfg.Reconciler.Schedule(domain.ID)
				}
			}
		}
	}

	// Inline SSL (30s): ACME with self-signed fallback. Never errors — cert
	// state is already in DB. JAB-233: automation skips this (SkipInlineSSL);
	// the first reconciler tick bootstraps the cert instead.
	if !attachedShared && !in.SkipInlineSSL && h.cfg.Reconciler != nil {
		inlineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		h.cfg.Reconciler.ReconcileSSLInline(inlineCtx, domain)
		cancel()
	}

	// Auto-enable email (best-effort, ADR-0013). A failure degrades to
	// email_enabled=0 + the UI retry switch; not returned in the response.
	if mailProvider == models.MailProviderJabali && h.cfg.Agent != nil && h.cfg.DNSZones != nil && h.cfg.DNSRecords != nil {
		if _, _, warnings, err := EnableDomainEmailInline(ctx, enableDomainEmailDeps{
			Agent:          h.cfg.Agent,
			Domains:        h.cfg.Domains,
			DNSZones:       h.cfg.DNSZones,
			DNSRecords:     h.cfg.DNSRecords,
			ServerSettings: h.cfg.ServerSettings,
			SSLCerts:       h.cfg.SSLCerts,
			SSLReconciler:  h.cfg.Reconciler,
		}, domain); err != nil {
			slog.Warn("auto-enable email failed during domain.create (operator can retry from UI)",
				"domain_id", domain.ID, "domain", domain.Name, "err", err)
		} else if len(warnings) > 0 {
			slog.Info("auto-enable email DNS autoconfig warnings",
				"domain_id", domain.ID, "domain", domain.Name, "warnings", warnings)
		}
	}

	// Schedule reconciliation — converges OS-level state (vhost, PHP pool, …)
	// with DB state. Non-blocking, out-of-band.
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}

	return domain, nil
}
