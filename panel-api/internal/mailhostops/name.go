package mailhostops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Name refusals. Each message is shown to the admin as is.
var (
	ErrNameIsPanelHostname = errors.New("the mail hostname must differ from the panel hostname")
	ErrNameAlreadyApplied  = errors.New("that is already the mail hostname")
	ErrNameClaimedByDomain = errors.New("a hosted domain answers or controls that name")
	ErrNameIsAlias         = errors.New("that name is a web alias of a hosted domain")
	ErrNameHasAliasUnder   = errors.New("a web alias of a hosted domain is under that name")
)

// errNameDepsUnwired: CheckName cannot clear a name it cannot look up.
var errNameDepsUnwired = errors.New("mailhostops: domain lookup is not wired")

// DomainFinder looks up a hosted domain by name (a free name returns
// repository.ErrNotFound) and the hosted domains under a name.
type DomainFinder interface {
	FindByName(ctx context.Context, name string) (*models.Domain, error)
	FindStrictSubdomains(ctx context.Context, name string) ([]models.Domain, error)
}

// AliasFinder looks up tenant web aliases: one by hostname (a free name
// returns repository.ErrNotFound), and the hostnames under a name.
type AliasFinder interface {
	domainops.AliasHostnameFinder
	FindStrictSubdomainHostnames(ctx context.Context, name string) ([]string, error)
}

// NameDeps are CheckName's lookups. Aliases may be nil when web aliases are
// not wired on this host.
type NameDeps struct {
	Domains DomainFinder
	Aliases AliasFinder
}

// CheckName refuses a mail hostname that the panel cannot own: the panel
// hostname, a name a hosted domain answers, whose zone a tenant controls or
// under which a tenant hosts a domain (domainops.MailHostnameConflict), or a
// tenant's web alias. desired must
// already be normalized (models.ValidateMailHostname). Any lookup error
// refuses: a name that could not be cleared is never applied.
func CheckName(ctx context.Context, deps NameDeps, s *models.ServerSettings, desired string) error {
	if deps.Domains == nil {
		return errNameDepsUnwired
	}
	if s != nil && strings.EqualFold(desired, s.Hostname) {
		return ErrNameIsPanelHostname
	}
	for _, name := range append([]string{desired}, domainops.AncestorDomains(desired)...) {
		d, err := deps.Domains.FindByName(ctx, name)
		switch {
		case errors.Is(err, repository.ErrNotFound):
		case err != nil:
			return fmt.Errorf("domain lookup for %q: %w", name, err)
		case d != nil && domainops.MailHostnameConflict(d.Name, desired, d.IsPanelPrimary):
			return fmt.Errorf("%w: %s", ErrNameClaimedByDomain, d.Name)
		}
	}
	under, err := deps.Domains.FindStrictSubdomains(ctx, desired)
	if err != nil {
		return fmt.Errorf("lookup of domains under %q: %w", desired, err)
	}
	for _, d := range under {
		if domainops.MailHostnameConflict(d.Name, desired, d.IsPanelPrimary) {
			return fmt.Errorf("%w: %s", ErrNameClaimedByDomain, d.Name)
		}
	}
	if deps.Aliases != nil {
		a, err := deps.Aliases.FindByHostname(ctx, desired)
		switch {
		case errors.Is(err, repository.ErrNotFound):
		case err != nil:
			return fmt.Errorf("alias lookup for %q: %w", desired, err)
		case a != nil:
			return ErrNameIsAlias
		}
		// An alias under the name is a tenant vhost that could set cookies
		// for it, the same reason a tenant domain there is refused.
		under, err := deps.Aliases.FindStrictSubdomainHostnames(ctx, desired)
		if err != nil {
			return fmt.Errorf("lookup of aliases under %q: %w", desired, err)
		}
		if len(under) > 0 {
			return fmt.Errorf("%w: %s", ErrNameHasAliasUnder, under[0])
		}
	}
	return nil
}
