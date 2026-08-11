package reconciler

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/hostedsvc"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dns01"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// JAB-235 — DNS-01 fallback for CDN-fronted domains.
//
// A domain whose apex resolves to a CDN (Cloudflare orange-cloud and kin)
// cannot pass HTTP-01: the edge answers the challenge path itself (403/404)
// and one failed name fails the whole cert, so the origin is stranded on
// self-signed. DNS-01 sidesteps the HTTP path entirely — the TXT record
// just has to land wherever the zone is REALLY hosted: our own PowerDNS
// when we are authoritative, or the customer's Cloudflare zone through the
// operator's stored API token. Domains with neither provider are parked
// with a clear reason and re-checked daily — never retried against LE
// (whose failed-authorization quota is what melted the aramapp fleet).

const (
	// dns01IssueTimeout bounds one certbot DNS-01 round trip: two hook
	// invocations with settle sleeps per name plus LE's validation poll.
	// Same budget as the shared-cert sweep (acmeSharedIssueTimeout) — the
	// HTTP-01 path's 60s would guarantee a context-deadline failure here.
	dns01IssueTimeout = 4 * time.Minute
	// dns01NoProviderRecheck paces re-checks of a fronted domain with no
	// available DNS provider. Daily: the fix is an operator/customer action
	// (store a token, grant zone access, move NS), not something that
	// changes minute-to-minute, and no LE quota is involved either way.
	dns01NoProviderRecheck = 24 * time.Hour
	// dns01RouteCacheTTL bounds how long an NS-delegation decision is
	// reused before re-asking public DNS. In-memory (not persisted on the
	// cert row as the ticket sketched): it survives across ticks in the
	// long-lived reconciler process and self-heals after NS changes.
	dns01RouteCacheTTL = time.Hour
)

// IssueMethod values recorded on ssl_certificates rows.
const (
	issueMethodHTTP01 = "http-01"
	issueMethodDNS01  = "dns-01"
)

type dns01CachedRoute struct {
	route   dns01.Route
	expires time.Time
}

// WithDNS01Key wires the sso key that unseals server_settings.cf_api_token_enc
// for the Cloudflare DNS-01 path. Nil-safe: without it, Cloudflare-hosted
// zones simply report "no token configured".
func (r *Reconciler) WithDNS01Key(key *ssokey.Key) *Reconciler {
	r.dns01SSOKey = key
	return r
}

// frontedByCDN reports whether the apex is served from somewhere that is
// not this box. Requires POSITIVE evidence on both sides — a failed apex
// lookup or unknown public IPs keeps the domain on HTTP-01 exactly as
// before (narrow, never invent a reason to change behaviour — the
// sanReachability philosophy).
func frontedByCDN(apexAddrs, ourAddrs []string) bool {
	if len(apexAddrs) == 0 || len(ourAddrs) == 0 {
		return false
	}
	return !intersects(apexAddrs, ourAddrs)
}

// dns01Route decides (with a TTL cache) who can host the _acme-challenge
// record for name. The same dns01.AuthoritativeRoute powers the acme-hook
// CLI, so the routing decision and the record write can never disagree.
func (r *Reconciler) dns01Route(ctx context.Context, srv *models.ServerSettings, name string) dns01.Route {
	r.dns01RouteMu.Lock()
	if cached, ok := r.dns01RouteCache[name]; ok && time.Now().Before(cached.expires) {
		r.dns01RouteMu.Unlock()
		return cached.route
	}
	r.dns01RouteMu.Unlock()

	cfg := dns01.Config{
		LookupNS:   dnsverify.LookupNSExternal,
		OurNSHosts: []string{srv.NS1Name, srv.NS2Name},
	}
	if r.dns01LookupNS != nil { // test seam
		cfg.LookupNS = r.dns01LookupNS
	}
	if len(srv.CFAPITokenEnc) > 0 && r.dns01SSOKey != nil {
		if tok, err := r.dns01SSOKey.Open(srv.CFAPITokenEnc); err == nil {
			cfg.CF = hostedsvc.NewCloudflareAPI(string(tok))
		} else {
			r.log.Error("ssl: cannot unseal the Cloudflare API token — DNS-01 via Cloudflare unavailable", "err", err)
		}
	}
	if r.dns01ZoneFinder != nil { // test seam
		cfg.CF = r.dns01ZoneFinder
	}

	routeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	route := dns01.AuthoritativeRoute(routeCtx, cfg, name)
	cancel()

	r.dns01RouteMu.Lock()
	if r.dns01RouteCache == nil {
		r.dns01RouteCache = map[string]dns01CachedRoute{}
	}
	r.dns01RouteCache[name] = dns01CachedRoute{route: route, expires: time.Now().Add(dns01RouteCacheTTL)}
	r.dns01RouteMu.Unlock()
	return route
}

// tryDNS01OrPark handles the LE branch for a fronted domain: issue via
// DNS-01 when a provider is available, else park with a clear reason.
//
// Parking mirrors selfSignAndWaitForDNS: self-signed fallback so HTTPS
// answers, pending_acme_retry with retry_count UNCHANGED (no ACME attempt
// was made, so no LE quota was spent and the #1049 cap must not tick), and
// a daily re-check. A real DNS-01 failure, by contrast, goes through
// fallbackToSelfSignAndRetry and counts toward the cap like any other
// spent LE attempt.
func (r *Reconciler) tryDNS01OrPark(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate, srv *models.ServerSettings, staging bool) {
	route := r.dns01Route(ctx, srv, domain.Name)
	if route.Provider == dns01.ProviderNone {
		fallbackCertPath, fallbackKeyPath, fallbackExpiresAt := r.ensureSelfSignFallback(ctx, domain, cert)
		nextRetry := time.Now().UTC().Add(dns01NoProviderRecheck)
		reason := "fronted by a CDN/proxy, and DNS-01 is not available: " + route.Reason +
			" — HTTP-01 cannot reach this origin, so Let's Encrypt is not attempted (no quota spent); re-checked daily"
		_ = r.sslCerts.UpdateAfterACMEFailure(ctx, cert.ID, reason, nextRetry, cert.RetryCount, fallbackCertPath, fallbackKeyPath, fallbackExpiresAt)
		r.log.Warn("ssl: fronted domain has no DNS-01 provider — parked",
			"domain", domain.Name, "reason", route.Reason, "next_check_at", nextRetry.Format(time.RFC3339))
		return
	}

	issueCtx, cancel := context.WithTimeout(ctx, dns01IssueTimeout)
	defer cancel()
	// Same lineage name as the HTTP-01 path (--cert-name <domain>), so the
	// two methods share one certbot lineage: certbot persists the manual
	// hooks in the renewal conf and `certbot renew` / ssl.renew keeps
	// renewing it unattended, whichever method issued last.
	raw, err := r.agent.Call(issueCtx, "ssl.issue_dns01", map[string]any{
		"cert_name": domain.Name,
		"sans":      []string{domain.Name, "www." + domain.Name},
		"email":     srv.AdminEmail,
		"staging":   staging,
	})
	if err != nil {
		r.log.Warn("ssl: dns-01 issue failed", "domain", domain.Name, "provider", route.Provider, "zone", route.Zone, "err", err)
		r.fallbackToSelfSignAndRetry(ctx, domain, cert, firstLine(err.Error()))
		return
	}
	issued, expires, ok := parseSSLIssueResult(raw, r.log, domain.Name)
	if !ok {
		msg := "agent returned unparseable ssl.issue_dns01 result"
		_ = r.sslCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusFailed, &msg)
		return
	}
	var res sslIssueResult
	_ = json.Unmarshal(raw, &res)
	if err := r.sslCerts.UpdateAfterIssuance(ctx, cert.ID, issued, expires, res.CertPath, res.KeyPath); err != nil {
		r.log.Error("ssl: write dns-01 issuance failed", "domain", domain.Name, "err", err)
		return
	}
	_ = r.sslCerts.SetIssueMethod(ctx, cert.ID, issueMethodDNS01)
	r.log.Info("ssl: issued via dns-01", "domain", domain.Name, "provider", string(route.Provider), "zone", route.Zone, "expires_at", expires.Format(time.RFC3339))
}

// dns01 routing state on the Reconciler (fields declared here to keep the
// JAB-235 surface in one file; the struct lives in reconciler.go).
type dns01State struct {
	dns01SSOKey     *ssokey.Key
	dns01RouteMu    sync.Mutex
	dns01RouteCache map[string]dns01CachedRoute
	// JAB-237 fronted-apex cache for the vhost path — see apexFronted.
	frontedMu    sync.Mutex
	frontedCache map[string]frontedEntry
	// test seams — nil in production
	dns01LookupNS   func(ctx context.Context, name string) ([]string, bool)
	dns01ZoneFinder dns01.ZoneFinder
}

// frontedCacheTTL bounds how long an apex-fronted decision is reused by the
// vhost renderer before re-asking public DNS.
const frontedCacheTTL = 15 * time.Minute

type frontedEntry struct {
	fronted bool
	expires time.Time
}

// apexFronted reports whether name's apex publicly resolves somewhere that
// is not this box — the vhost renderer's view of frontedByCDN (JAB-237).
//
// Cache rule, load-bearing: a DEFINITIVE lookup (public DNS answered)
// updates the cache; an INCONCLUSIVE one (every resolver unreachable) keeps
// the last known value and extends its TTL. Without that, a resolver blip
// would flip fronted domains to "not fronted", re-render their vhosts
// :80-only, and 520 every Cloudflare-Full zone until the resolvers return —
// the exact outage this fix exists for. A cold cache + inconclusive answer
// falls back to "not fronted" (the pre-JAB-237 rendering); accepted
// residual, called out in the PR.
func (r *Reconciler) apexFronted(ctx context.Context, name string) bool {
	now := time.Now()
	r.frontedMu.Lock()
	entry, cached := r.frontedCache[name]
	r.frontedMu.Unlock()
	if cached && now.Before(entry.expires) {
		return entry.fronted
	}

	var ourAddrs []string
	if r.serverSettings != nil {
		srvCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if srv, err := r.serverSettings.Get(srvCtx); err == nil && srv != nil {
			if srv.PublicIPv4 != "" {
				ourAddrs = append(ourAddrs, srv.PublicIPv4)
			}
			if srv.PublicIPv6 != "" {
				ourAddrs = append(ourAddrs, srv.PublicIPv6)
			}
		}
		cancel()
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	addrs, queried := r.dnsPreflight(lookupCtx, name)
	cancel()

	if !queried || len(ourAddrs) == 0 {
		// Inconclusive (or we don't know our own IP): keep the last known
		// value alive rather than flapping; cold cache reads not-fronted.
		if cached {
			r.frontedMu.Lock()
			entry.expires = now.Add(frontedCacheTTL)
			r.frontedCache[name] = entry
			r.frontedMu.Unlock()
			return entry.fronted
		}
		return false
	}

	fronted := frontedByCDN(addrs, ourAddrs)
	r.frontedMu.Lock()
	if r.frontedCache == nil {
		r.frontedCache = map[string]frontedEntry{}
	}
	r.frontedCache[name] = frontedEntry{fronted: fronted, expires: now.Add(frontedCacheTTL)}
	r.frontedMu.Unlock()
	return fronted
}
