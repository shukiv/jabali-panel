package domainops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var (
	// ErrDomainConflictsMailHostname: the name is the panel hostname, the
	// derived mail.<hostname>, or the panel's custom mail hostname, a parent
	// zone of it, or a name under it (JAB-390).
	ErrDomainConflictsMailHostname = errors.New("domainops: the name conflicts with the panel's hostname or mail hostname")
	// ErrMailHostnameLookup wraps a settings read error (the guard fails
	// closed).
	ErrMailHostnameLookup = errors.New("domainops: panel mail hostname lookup failed")
)

// panelPrimaryReservedPrefixes are the panel-primary domain's server names
// that belong to vhosts other than its mail vhost. mail.<domain> is not
// here: it is the panel's derived mail hostname.
var panelPrimaryReservedPrefixes = []string{"www.", "autoconfig.", "autodiscover.", "mta-sts."}

// MailHostnameConflict reports whether a hosted domain would answer, or
// control the DNS of, the panel mail hostname host (JAB-390).
//
// Every hosted domain but the panel-primary one is tenant-owned (admins
// cannot host domains). A tenant domain conflicts when it is host or a parent
// zone of host: its apex and helper vhosts answer those names, and a tenant
// who controls the zone could repoint the name or obtain a certificate for it
// elsewhere. It also conflicts when it is a name under host: a tenant site
// there can set cookies scoped to host, which webmail on host would accept,
// and can pass itself off as the panel's mail service. The derived
// mail.<hostname> gets the same protection from CrossTenantSuffixCollision,
// because it sits under the admin-owned panel-primary domain. Delegation
// (allow_subdomain_delegation) does not change this: it lets other tenants
// nest domains, not take over the panel's mail identity.
//
// The panel-primary domain is admin-owned. It conflicts only when host is
// the domain itself or one of its non-mail server names; its mail vhost
// serves the panel mail hostname by design.
func MailHostnameConflict(domain, host string, panelPrimary bool) bool {
	domain, host = NormalizeDomainName(domain), NormalizeDomainName(host)
	if domain == "" || host == "" {
		return false
	}
	if host == domain {
		return true
	}
	if !panelPrimary {
		return strings.HasSuffix(host, "."+domain) || strings.HasSuffix(domain, "."+host)
	}
	for _, p := range panelPrimaryReservedPrefixes {
		if host == p+domain {
			return true
		}
	}
	return false
}

// PanelReservedName reports whether name is one of the panel's own names:
// the panel hostname or the derived mail.<hostname>. Both are the panel's
// whether or not a panel-primary domain row exists (install.sh creates that
// row only with the dns module), and the derived name stays reserved after a
// switchover because the old name is served alongside the new one. Only the
// exact names are reserved: a parent zone of the panel hostname may be an
// admin's own site hosted as a tenant.
func PanelReservedName(name, panelHostname string) bool {
	host := strings.TrimSuffix(NormalizeDomainName(panelHostname), ".")
	name = strings.TrimSuffix(NormalizeDomainName(name), ".")
	if host == "" || name == "" {
		return false
	}
	return name == host || name == models.PanelMailHostname(host)
}

// MailHostnameCollision reports whether a tenant claiming the domain name
// (create, rename, docker app hostname) would conflict with the panel's
// names: the panel hostname or the derived mail.<hostname> exactly
// (PanelReservedName), or the applied custom mail hostname by
// MailHostnameConflict. A nil reader means the settings are unwired; a
// missing settings row means nothing is set. Any other read error is
// returned so the caller fails closed.
//
// A requested but not yet applied name is protected by the switchover
// engine, which re-checks for a conflicting domain before it issues and again
// before it applies the name.
func MailHostnameCollision(ctx context.Context, settings MailSettingsReader, name string) (bool, error) {
	if settings == nil {
		return false, nil
	}
	s, err := settings.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read panel mail hostname: %w", err)
	}
	if s == nil {
		return false, nil
	}
	if PanelReservedName(name, s.Hostname) {
		return true, nil
	}
	applied, ok := models.AppliedMailHostname(s.MailHostname)
	if !ok {
		return false, nil
	}
	return MailHostnameConflict(name, applied, false), nil
}
