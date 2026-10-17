// Package services hosts thin orchestration helpers that don't fit a
// single repository or HTTP handler. The panel cert routability check
// lives here so the REST handler and the reconciler can share one
// implementation without dragging GORM into either.
package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
)

// RoutabilityResult tells the caller why the panel hostname does (or
// does not) qualify for a Let's Encrypt issuance attempt.
type RoutabilityResult struct {
	Routable bool
	// Reason is a short, UI-displayable string. Empty when Routable=true.
	Reason string
}

// Resolver is the single dependency PanelCertRoutability has on the
// outside world. Production wiring uses net.DefaultResolver; tests
// inject a stub that returns canned answers without DNS round-trips.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// PanelCertRoutability decides whether a panel hostname is publicly
// routable enough to attempt LE HTTP-01. It deliberately does NOT
// probe port 80 from the panel-api side: a successful TCP probe from
// the panel host doesn't prove that the LE validation server (which
// connects from outside) can reach :80, and a failed probe can't
// distinguish "really blocked" from "lo only" weirdness on multi-NIC
// VPS images. Port-80 reachability is left to certbot, which
// surfaces the real LE error in last_error if validation fails.
type PanelCertRoutability struct {
	Resolver Resolver
	// PublicLookup resolves a name via external public resolvers and
	// reports whether any gave a definitive answer (see
	// dnsverify.LookupHostExternalResult). Used on the requirePublic
	// path and when the local lookup answers only loopback (/etc/hosts).
	// Defaults to dnsverify.LookupHostExternalResult; tests inject a stub.
	PublicLookup func(ctx context.Context, host string) (addrs []string, queried bool)
}

// NewPanelCertRoutability wires the production resolver
// (net.DefaultResolver). Pass a stubbed Resolver for tests.
func NewPanelCertRoutability() *PanelCertRoutability {
	return &PanelCertRoutability{
		Resolver:     net.DefaultResolver,
		PublicLookup: dnsverify.LookupHostExternalResult,
	}
}

// Check runs the gate. hostname is the panel's canonical FQDN
// (server_settings.hostname); publicIPv4 is the VPS's outward-facing
// address (server_settings.public_ipv4). Both come from the same row
// the admin manages on the Settings page.
//
// Skip rules, in order:
//
//  1. empty hostname / empty public_ipv4 → not routable; reason
//     "missing hostname" or "missing public_ipv4".
//  2. hostname matches localhost / *.local / *.localdomain → not
//     routable; reason "non-routable hostname suffix". Browsers and
//     LE both refuse these.
//  3. DNS A lookup returns nothing or fails → not routable; reason
//     "dns lookup failed: …". Only loopback (an /etc/hosts line) →
//     public DNS is asked instead; no public address → not routable,
//     with a reason that names /etc/hosts.
//  4. None of the resolved addresses match publicIPv4 → not routable;
//     reason "dns points elsewhere (got X.X.X.X, want Y.Y.Y.Y); …",
//     with a hint for a CDN-proxied record.
//
// Otherwise routable.
func (p *PanelCertRoutability) Check(ctx context.Context, hostname, publicIPv4 string, requirePublic bool) (RoutabilityResult, error) {
	host := strings.TrimSpace(strings.ToLower(hostname))
	if host == "" {
		return RoutabilityResult{Routable: false, Reason: "missing hostname"}, nil
	}
	if publicIPv4 == "" {
		return RoutabilityResult{Routable: false, Reason: "missing public_ipv4"}, nil
	}

	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localdomain") {
		return RoutabilityResult{Routable: false, Reason: "non-routable hostname suffix"}, nil
	}

	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return RoutabilityResult{Routable: false, Reason: fmt.Sprintf("dns lookup failed: %v", err)}, nil
	}
	if len(addrs) == 0 {
		return RoutabilityResult{Routable: false, Reason: "dns lookup returned no records"}, nil
	}

	// /etc/hosts shadow guard: if every result is loopback (Debian
	// seeds `127.0.1.1 <hostname>` on first boot and never strips it),
	// the default resolver is misleading us — public DNS likely
	// resolves elsewhere. Ask external public DNS resolvers instead
	// (Cloudflare, Quad9, Google; UDP then TCP so a carrier that blocks
	// outbound UDP/53 still gets an answer), which never read
	// /etc/hosts (GH #2166). The reason then reports what public DNS
	// said; the loopback address is never shown as the DNS answer.
	//
	// install.sh strips the stale 127.0.1.1 line on every run, but
	// this guard catches operators who edit /etc/hosts by hand AND
	// keeps the gate robust on hosts where install.sh hasn't been
	// re-run since the strip landed.
	public := false
	if onlyLoopback(addrs) {
		extAddrs, queried := p.publicLookup()(ctx, host)
		switch {
		case len(extAddrs) > 0:
			addrs, public = extAddrs, true
		case queried:
			return RoutabilityResult{Routable: false, Reason: fmt.Sprintf(
				"no public A record: %s resolves to %s only through this server's /etc/hosts, and public DNS has no address for it",
				host, strings.Join(addrs, ","))}, nil
		default:
			return RoutabilityResult{Routable: false, Reason: fmt.Sprintf(
				"%s resolves to %s only through this server's /etc/hosts, and public DNS could not be reached to check it",
				host, strings.Join(addrs, ","))}, nil
		}
	}

	// Walk the response set. We only consider IPv4 matches for the
	// panel-cert routability gate; an AAAA-only result on a panel
	// reachable via IPv6 still falls back to self-signed for now.
	// IPv6-first deployments can override via the staging toggle in
	// M32.1.
	want := strings.TrimSpace(publicIPv4)
	var ipv4Got []string
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if ip.To4() == nil {
			continue
		}
		ipv4Got = append(ipv4Got, ip.String())
		if ip.String() == want {
			if !requirePublic || public {
				return RoutabilityResult{Routable: true}, nil
			}
			return p.verifyPublic(ctx, host, want), nil
		}
	}

	if len(ipv4Got) == 0 {
		return RoutabilityResult{Routable: false, Reason: "dns lookup returned no IPv4 records"}, nil
	}
	return RoutabilityResult{
		Routable: false,
		Reason: fmt.Sprintf(
			"dns points elsewhere (got %s, want %s); if %s is proxied by a CDN such as Cloudflare, set its record to DNS only: Let's Encrypt can't validate the panel through the proxy",
			strings.Join(ipv4Got, ","), want, host),
	}, nil
}

// publicLookup is PublicLookup, or the production external lookup.
func (p *PanelCertRoutability) publicLookup() func(ctx context.Context, host string) ([]string, bool) {
	if p.PublicLookup != nil {
		return p.PublicLookup
	}
	return dnsverify.LookupHostExternalResult
}

// verifyPublic confirms that external public resolvers also see `host`
// resolving to `want`. The local resolver already matched (the box
// answers from its own authoritative PDNS even for a domain whose
// nameservers aren't delegated here), which says nothing about what the
// Let's Encrypt validator will see. Decision table:
//
//	public shows want            -> routable (the world agrees).
//	public reachable, !want/none -> NOT routable, actionable reason
//	                                (this is the silent self-signed case:
//	                                mail.<hostname> never delegated).
//	public unreachable (egress)  -> routable; we can't prove the negative,
//	                                so fall back to the local view and let
//	                                certbot be the judge (its error is then
//	                                humanized by HumanizePanelCertError).
func (p *PanelCertRoutability) verifyPublic(ctx context.Context, host, want string) RoutabilityResult {
	extAddrs, queried := p.publicLookup()(ctx, host)
	if !queried {
		return RoutabilityResult{Routable: true}
	}
	for _, a := range extAddrs {
		if a == want {
			return RoutabilityResult{Routable: true}
		}
	}
	got := "no public A record (NXDOMAIN)"
	if len(extAddrs) > 0 {
		got = "public DNS -> " + strings.Join(extAddrs, ",")
	}
	return RoutabilityResult{
		Routable: false,
		Reason: fmt.Sprintf(
			"%s resolves locally to %s but %s; delegate this domain's nameservers to this server (ns1/ns2) or add a public A record for %s",
			host, want, got, host),
	}
}

// HumanizePanelCertError rewrites a raw certbot / Let's Encrypt error into
// an operator-actionable message when it signals the public-DNS gap that
// silently keeps mail.<hostname> on a self-signed cert. Non-DNS errors
// (rate limit, port 80 unreachable, etc.) pass through unchanged. Used as
// the backstop for the case verifyPublic returned inconclusive (egress
// blocked) but certbot then failed against the real authoritative NS.
func HumanizePanelCertError(name, raw string) string {
	low := strings.ToLower(raw)
	for _, marker := range []string{
		"dns problem", "nxdomain", "no valid a records",
		"acme:error:dns", "servfail", "no a record",
	} {
		if strings.Contains(low, marker) {
			return fmt.Sprintf(
				"%s is not resolvable from public DNS (certbot: %s); delegate this domain's nameservers to this server (ns1/ns2) or add a public A record for %s",
				name, raw, name)
		}
	}
	return raw
}

// ErrNoResolver is returned by helpers that need a resolver and got
// nil. Currently only used by tests to assert the constructor wires
// a sane default.
var ErrNoResolver = errors.New("nil Resolver")

// onlyLoopback returns true when every IP in addrs is loopback (127/8
// or ::1). Used by Check to detect /etc/hosts shadowing on Debian
// hosts where `127.0.1.1 <hostname>` survived a hostname change.
func onlyLoopback(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			return false
		}
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}
