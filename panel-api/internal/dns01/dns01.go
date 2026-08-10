// Package dns01 decides where a domain's ACME DNS-01 challenge must be
// written: the panel's own PowerDNS, the customer's Cloudflare zone via the
// stored API token, or nowhere (no provider available). The decision is
// anchored on the PUBLIC NS delegation, never on local dns_zones rows — the
// reconciler auto-creates a zone row for every domain, so a migrated domain
// whose DNS actually lives at Cloudflare still carries a local row; writing
// the TXT there would leave Let's Encrypt querying Cloudflare for a record
// that only exists in our pdns (JAB-235).
//
// Both the reconciler (challenge routing) and the `jabali dns acme-hook`
// CLI (record writing) use this ONE decider, so they can never disagree on
// the stale-zone case.
package dns01

import (
	"context"
	"fmt"
	"strings"
)

// Provider says who can host the _acme-challenge record for a name.
type Provider string

const (
	ProviderNone       Provider = "none"
	ProviderPDNS       Provider = "pdns"
	ProviderCloudflare Provider = "cloudflare"
)

// cloudflareNSSuffix identifies Cloudflare-assigned nameservers
// (<name>.ns.cloudflare.com).
const cloudflareNSSuffix = ".ns.cloudflare.com"

// ZoneFinder resolves a Cloudflare zone id for an EXACT zone name.
// Returns "" (no error) when the stored token does not cover the zone.
type ZoneFinder interface {
	FindZoneID(ctx context.Context, name string) (string, error)
}

// Config carries the decision inputs. LookupNS is dnsverify.LookupNSExternal
// in production; tests substitute a table. CF is nil when no Cloudflare API
// token is configured.
type Config struct {
	LookupNS   func(ctx context.Context, name string) (hosts []string, queried bool)
	OurNSHosts []string // server_settings ns1/ns2 names, matched case-insensitively
	CF         ZoneFinder
}

// Route is the outcome: which provider owns the zone that serves name, and —
// for Cloudflare — the zone id to write into. Reason explains ProviderNone
// (and is written into cert last_error so the operator sees WHY a domain
// cannot auto-issue instead of a silent skip).
type Route struct {
	Provider Provider
	Zone     string // the delegation point the decision anchored on
	CFZoneID string
	Reason   string
}

// AuthoritativeRoute walks name's parent chain (name, then each parent,
// stopping at 2 labels — same walk as the hook's findZoneForName) and asks
// public resolvers for the NS RRset. The first non-empty answer is the zone
// apex; its NS hosts classify the provider.
//
// Fail-open rule: if every external resolver is unreachable the answer is
// inconclusive — callers must keep their pre-JAB-235 behaviour (the hook
// falls back to the local-zone path, the reconciler stays on HTTP-01), so
// that a resolver outage on our side never strands issuance.
func AuthoritativeRoute(ctx context.Context, cfg Config, name string) Route {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(name), "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		zone := strings.Join(labels[i:], ".")
		hosts, queried := cfg.LookupNS(ctx, zone)
		if !queried {
			return Route{Provider: ProviderNone, Reason: "external DNS resolvers unreachable — cannot determine the authoritative nameservers"}
		}
		if len(hosts) == 0 {
			continue // below the zone apex — walk up
		}
		return classify(ctx, cfg, zone, hosts)
	}
	return Route{Provider: ProviderNone, Reason: fmt.Sprintf("no NS delegation found for %s in public DNS", name)}
}

func classify(ctx context.Context, cfg Config, zone string, hosts []string) Route {
	// Normalize defensively even though LookupNSExternal already lowercases:
	// Config.LookupNS is pluggable and NS hosts compare case-insensitively.
	normalized := make([]string, 0, len(hosts))
	for _, h := range hosts {
		normalized = append(normalized, strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), ".")))
	}
	hosts = normalized
	if hostsHaveSuffix(hosts, cloudflareNSSuffix) {
		if cfg.CF == nil {
			return Route{Provider: ProviderNone, Zone: zone,
				Reason: fmt.Sprintf("zone %s is hosted at Cloudflare but no Cloudflare API token is configured (Settings → SSL)", zone)}
		}
		id, err := cfg.CF.FindZoneID(ctx, zone)
		if err != nil {
			return Route{Provider: ProviderNone, Zone: zone,
				Reason: fmt.Sprintf("zone %s: Cloudflare zone lookup failed: %v", zone, err)}
		}
		if id == "" {
			return Route{Provider: ProviderNone, Zone: zone,
				Reason: fmt.Sprintf("zone %s is hosted at Cloudflare but the configured API token has no access to it — the customer must grant the token access (or move NS)", zone)}
		}
		return Route{Provider: ProviderCloudflare, Zone: zone, CFZoneID: id}
	}
	if hostsMatchOurs(hosts, cfg.OurNSHosts) {
		return Route{Provider: ProviderPDNS, Zone: zone}
	}
	return Route{Provider: ProviderNone, Zone: zone,
		Reason: fmt.Sprintf("zone %s is delegated to %s — neither this panel's DNS nor a configured provider", zone, strings.Join(hosts, ", "))}
}

func hostsHaveSuffix(hosts []string, suffix string) bool {
	for _, h := range hosts {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// hostsMatchOurs reports whether ANY delegated NS host is one of this
// panel's nameserver names. Any-match (not all-match) on purpose: a zone
// with a mixed NS set that includes us still resolves through us with
// positive probability, and the pdns write is at worst a no-op for LE.
func hostsMatchOurs(hosts, ours []string) bool {
	if len(ours) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(ours))
	for _, o := range ours {
		o = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(o), "."))
		if o != "" {
			set[o] = struct{}{}
		}
	}
	for _, h := range hosts {
		if _, ok := set[h]; ok {
			return true
		}
	}
	return false
}
