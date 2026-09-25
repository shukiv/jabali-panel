package domainops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Cross-tenant name guards (GH #1625, GH #1789, GH #1812). A new or renamed
// domain name must not claim a server_name that another domain's web alias
// holds, and a tenant must not nest a zone under, or wrap a zone around, a
// domain another tenant owns. Both checks are the domain lifecycle module's
// (JAB-279): the create doors, the rename door and the docker-app doors call
// them here, so the candidate derivation and the fail-closed rule cannot drift
// between adapters.

// aliasHelperPrefixes are the auto-derived helper subdomains the panel serves
// for EVERY domain (its own vhost + mail/ACME server_name). An alias equal to
// "<prefix><some other domain>" would add a second nginx server block with the
// same server_name as that domain's helper vhost; nginx keeps the first and
// silently drops the rest, so the alias could shadow another tenant's mail or
// web vhost. Kept in lockstep with the agent's webmail_vhost server_name list
// and reconciler.sanHostnamesForDomain (GH #1625).
var aliasHelperPrefixes = []string{"www.", "mail.", "autoconfig.", "autodiscover.", "mta-sts."}

// AliasHelperPrefixes returns a copy of the helper-subdomain prefixes, so a
// caller cannot change the list the collision guard checks.
func AliasHelperPrefixes() []string {
	return append([]string(nil), aliasHelperPrefixes...)
}

// AliasHostnameFinder looks up a web-domain alias by its hostname. A free
// hostname returns repository.ErrNotFound.
type AliasHostnameFinder interface {
	FindByHostname(ctx context.Context, hostname string) (*models.WebDomainAlias, error)
}

// AliasCollision is the REVERSE of the alias handler's own helper check: it
// reports whether creating (or renaming to) a domain named `name` would claim
// an nginx server_name already held by another domain's web-domain alias. The
// alias handler stops an alias from colliding with an existing domain; without
// this, a domain created AFTER an alias with the same name reopens the exact
// cross-tenant vhost-hijack hole (a duplicate server_name across two server
// blocks silently lets the first-loaded win — nginx -t only warns). A domain
// named N claims N, www.N, and its four mail-helper server_names, so all six
// are checked against the alias table regardless of the domain's EmailEnabled:
// a domain can enable mail later and its helper vhost would then collide.
// Returns the colliding hostname and true on a hit, or a non-nil error when the
// lookup itself failed. A nil finder means the feature is unwired → no
// collision (fail-open ONLY when the feature is unwired).
//
// SECURITY: a live lookup error is NEVER swallowed into "no collision". Doing so
// would fail OPEN — a create/rename would claim a server_name the check could
// not clear, reopening the cross-tenant hijack this guard exists to close. Only
// repository.ErrNotFound (the name is free) advances the scan; any other error
// aborts with err != nil so every caller fails closed.
func AliasCollision(ctx context.Context, aliases AliasHostnameFinder, name string) (string, bool, error) {
	if aliases == nil {
		return "", false, nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", false, nil
	}
	candidates := make([]string, 0, len(aliasHelperPrefixes)+1)
	candidates = append(candidates, name)
	for _, p := range aliasHelperPrefixes {
		candidates = append(candidates, p+name)
	}
	for _, cand := range candidates {
		existing, err := aliases.FindByHostname(ctx, cand)
		switch {
		case err == nil:
			if existing != nil {
				return cand, true, nil
			}
		case errors.Is(err, repository.ErrNotFound):
			// the name is free — keep scanning the remaining candidates.
		default:
			return "", false, fmt.Errorf("alias lookup for %q: %w", cand, err)
		}
	}
	return "", false, nil
}

// SuffixDomainFinder is the slice of the domain repository
// CrossTenantSuffixCollision needs. FindByName returns repository.ErrNotFound
// for a free name.
type SuffixDomainFinder interface {
	FindByName(ctx context.Context, name string) (*models.Domain, error)
	FindStrictSubdomains(ctx context.Context, name string) ([]models.Domain, error)
}

// CrossTenantSuffixCollision reports whether a tenant claiming the domain
// `name` would land inside, or wrap around, a domain owned by a DIFFERENT
// tenant — the cross-tenant DNS subdomain-hijack the panel otherwise allows
// (GH #1789). It is ORTHOGONAL to AliasCollision: that guard checks the
// web-domain alias table (nginx server_name collisions); this one compares the
// new name against the domains table for a parent/subdomain suffix relationship,
// which no other check performs (the only uniqueness is exact-string
// ux_domains_name, and a subdomain string never collides with its parent).
//
// Two directions, both label-boundary aware so "evil.example.com" clashes with
// "example.com" but "notexample.com" never does:
//
//   - Parent: walk name's registrable ancestors (AncestorDomains) and look each
//     up by exact name. A hit owned by another tenant is a clash — the
//     claimant would be creating a more-specific zone under someone else's
//     domain (mail interception, phishing under a trusted name).
//   - Child: find every existing domain that is a strict subdomain of name. A
//     hit owned by another tenant is a clash — the claimant would be creating a
//     parent zone that wraps another tenant's subdomain.
//
// Same-owner matches are ALLOWED: a tenant may freely add subdomains of (or a
// parent over) their own domains. ownerID is the prospective owner of `name`.
//
// Delegated matches are ALLOWED in the PARENT direction only (GH #1812): if a
// differently-owned ancestor has AllowSubdomainDelegation set, its owner has
// consented to other tenants nesting under it, so that ancestor is not a clash.
// The child direction is unaffected — delegation grants nesting UNDER a domain,
// never the right to claim a parent zone OVER someone else's subdomain.
//
// A nil finder means the feature is unwired → no collision (fail-open ONLY when
// unwired, mirroring AliasCollision). Callers gate this on non-admin: admins are
// trusted to resolve legitimate cross-tenant delegation.
//
// SECURITY: a live lookup error is NEVER swallowed into "no collision" — it
// aborts with err != nil so every caller fails CLOSED, exactly as AliasCollision
// does. Only repository.ErrNotFound (an ancestor is free) advances the scan.
func CrossTenantSuffixCollision(ctx context.Context, domains SuffixDomainFinder, name, ownerID string) (string, bool, error) {
	if domains == nil {
		return "", false, nil
	}
	name = NormalizeDomainName(name)
	if name == "" {
		return "", false, nil
	}

	// Parent direction: is name a strict subdomain of a differently-owned zone?
	for _, anc := range AncestorDomains(name) {
		d, err := domains.FindByName(ctx, anc)
		switch {
		case err == nil:
			// A differently-owned ancestor is a hijack UNLESS its owner opted
			// that domain into subdomain delegation (GH #1812). The flag lives
			// on the ancestor row `d` — already loaded here, so no extra lookup
			// and no new fail-open path; a delegated ancestor is simply skipped.
			//
			// We do NOT short-circuit the walk on the first delegated ancestor:
			// every differently-owned ancestor must independently consent (or be
			// the claimant's own). So c.b.a.com claimed by a third tenant, where
			// b.a.com delegates but a.com does not, still clashes on a.com — each
			// non-delegated ancestor stays protected exactly as #1789 enforces.
			if d != nil && d.UserID != ownerID && !d.AllowSubdomainDelegation {
				return anc, true, nil
			}
		case errors.Is(err, repository.ErrNotFound):
			// this ancestor is unclaimed — keep walking up.
		default:
			return "", false, fmt.Errorf("ancestor lookup for %q: %w", anc, err)
		}
	}

	// Child direction: does name wrap a differently-owned subdomain? Return the
	// CLAIMANT's own name as the hit — never subs[i].Name. The conflicting
	// subdomain belongs to another tenant, and echoing it into the 409 body would
	// let an attacker who claims an unowned parent enumerate other tenants'
	// subdomains under it.
	subs, err := domains.FindStrictSubdomains(ctx, name)
	if err != nil {
		return "", false, fmt.Errorf("subdomain lookup for %q: %w", name, err)
	}
	for i := range subs {
		if subs[i].UserID != ownerID {
			return name, true, nil
		}
	}

	return "", false, nil
}
