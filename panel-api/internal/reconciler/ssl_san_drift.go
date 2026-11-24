package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dns01"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ssl_san_drift.go — JAB-226 tail. Converge an already-issued Let's Encrypt
// certificate whose SAN set is missing a name the domain's flags now say it
// should carry (the motivating case: autodiscover.<domain>, GH #185/#1039).
//
// Why this is needed: sanHostnamesForDomain is only consulted at ISSUANCE
// (reconcileSSLForDomain's tryACMEOrFallback). An `issued` cert has no dispatch
// case there, and ADR-0163 renewal is certbot's own timer, which reuses the
// existing SAN set — so a name that was dropped at issue time (e.g. its DNS
// hadn't propagated) never gets added. Outlook's autodiscover.<domain> probe
// then hits a TLS mismatch forever. Measured on jabali.site: cert had mail. +
// autoconfig. but not autodiscover., and a forced certbot renew did NOT add it.
//
// Safety (this pass touches certbot on a live cert, so it is deliberately timid):
//   - LE mode only. Custom / self / shared / none certs are skipped — running
//     certbot --cert-name against an operator's custom cert would clobber the LE
//     lineage (#738/#745).
//   - Wildcard certs skipped: a *.<domain> SAN already covers the helpers, and
//     exact-matching would churn an --expand every pass forever.
//   - reachable-filter BEFORE requesting: a name that can't answer HTTP-01 is
//     not requested, so we never burn LE's failed-authorization quota (JAB-226
//     is exactly that failure turned into a permanent outage).
//   - Per-pass cap + per-cert cooldown: at most sslSANDriftMaxPerPass expands an
//     hour, and a given cert is retried no more than once per sslSANDriftCooldown
//     — so a fleet converges over hours, never in one Let's-Encrypt burst.
//   - On failure the existing cert is left exactly as it was: no status flip, no
//     self-sign downgrade. The domain keeps serving its current (valid) cert.

const (
	// sslSANDriftMaxPerPass bounds Let's Encrypt reissues per hourly pass. Kept
	// low on purpose: the only cost of a small cap is how many hours a fleet
	// takes to converge; the cost of a big one is an LE rate-limit lockout.
	sslSANDriftMaxPerPass = 2
	// sslSANDriftCooldown is the minimum gap between reissue attempts for one
	// cert. Also gates the (cheaper) reachability probing, so a cert whose
	// missing SAN isn't reachable yet isn't re-probed every pass.
	sslSANDriftCooldown = 6 * time.Hour
)

// sslSANDriftMissing returns the desired SAN names an issued cert does not yet
// cover. Pure: the caller supplies the cert's actual SAN names. Returns nil when
// the cert already covers everything, carries a wildcard (assumed to cover the
// helpers — never churn it), or the domain wants no extra SANs. Uses the domain
// flags carried on the ListAll row so no per-cert domain lookup is needed here.
func sslSANDriftMissing(row repository.SSLCertificateWithDomain, certSANs, aliases []string) []string {
	dom := &models.Domain{
		Name:          row.DomainName,
		EmailEnabled:  row.EmailEnabled,
		SkipAutoSAN:   row.SkipAutoSAN,
		CreateWWW:     row.CreateWWW,
		MTASTSEnabled: row.MTASTSEnabled,
	}
	desired := sanHostnamesForDomain(dom, aliases)
	if row.IssueMethod == issueMethodDNS01 {
		// A DNS-01 certificate carries its own policy (GH #2561): www.<name> and
		// the verified aliases, never the mail helpers — so a helper it will
		// never carry cannot hold one of the pass's slots.
		desired = dns01DesiredSANs(row.DomainName, aliases)[1:]
	}
	if len(desired) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(certSANs))
	for _, s := range certSANs {
		if strings.HasPrefix(s, "*.") {
			return nil // wildcard covers subdomains — treat as complete
		}
		have[strings.ToLower(s)] = struct{}{}
	}
	var missing []string
	for _, d := range desired {
		if _, ok := have[strings.ToLower(d)]; !ok {
			missing = append(missing, d)
		}
	}
	return missing
}

// sslSANDriftEligible is the cheap, no-IO gate: an issued LE cert with a cert
// file on disk. Custom/self/shared/none modes are excluded so certbot never
// touches a cert the panel doesn't own the ACME lineage for.
func sslSANDriftEligible(row repository.SSLCertificateWithDomain) bool {
	if row.Status != models.SSLStatusIssued || row.CertPath == nil || *row.CertPath == "" {
		return false
	}
	// A certificate a restore installed (GH #1993) isn't certbot's: issuing
	// clears its files first, and a failed attempt would leave the domain
	// without HTTPS. The hand-back (handBackRestoredCert) replaces it when
	// the name points here.
	if row.IssueMethod == models.SSLIssueMethodRestored {
		return false
	}
	return row.SSLMode == models.SSLModeLE || row.SSLMode == ""
}

// acmeStaging reports whether a top-up goes to Let's Encrypt staging: when the
// row says so, or when the box is set to staging (JABALI_ACME_STAGING_ONLY) the
// way its first issuance reads it. That issuance never stores the flag on the
// row, so the row alone sent a staging box's top-up to production.
func (r *Reconciler) acmeStaging(rowStaging bool) bool {
	return rowStaging || (r.cfg != nil && r.cfg.ACME.StagingOnly)
}

// sanDriftStamp is one cert's last SAN-drift attempt: when, and which
// missing names it tried.
type sanDriftStamp struct {
	at    time.Time
	names map[string]bool
}

// sanDriftCooldownPassed reports whether this cert may be attempted again for
// these missing names, and (when true) is expected to be paired with
// markSANDriftAttempt by the caller. A missing name the last attempt didn't
// try (www switched on, an alias added) passes at once: the cooldown is for
// names that were already found unreachable, and only growth resets it, so a
// shrinking set (a name dropped or obtained) still waits.
func (r *Reconciler) sanDriftCooldownPassed(certID string, missing []string, now time.Time) bool {
	r.sanDriftMu.Lock()
	defer r.sanDriftMu.Unlock()
	last, ok := r.sanDriftAttempt[certID]
	if !ok || now.Sub(last.at) >= sslSANDriftCooldown {
		return true
	}
	for _, n := range missing {
		if !last.names[n] {
			return true
		}
	}
	return false
}

func (r *Reconciler) markSANDriftAttempt(certID string, missing []string, now time.Time) {
	r.sanDriftMu.Lock()
	defer r.sanDriftMu.Unlock()
	if r.sanDriftAttempt == nil {
		r.sanDriftAttempt = map[string]sanDriftStamp{}
	}
	names := make(map[string]bool, len(missing))
	for _, n := range missing {
		names[n] = true
	}
	r.sanDriftAttempt[certID] = sanDriftStamp{at: now, names: names}
}

// KickSANDrift asks the reconcile loop for a SAN-drift pass now instead of at
// the next hourly tick (a domain's desired names just grew). Non-blocking;
// kicks coalesce. The pass itself still applies the per-cert cooldown.
func (r *Reconciler) KickSANDrift() {
	select {
	case r.sanDriftKick <- struct{}{}:
	default:
	}
}

// ReconcileSSLSANDrift finds issued LE certs missing a reachable desired SAN and
// reissues them (certbot --expand) to add it. Called on the hourly ssl_observe
// cadence (and once at startup). Rate-limited internally; see the file header.
func (r *Reconciler) ReconcileSSLSANDrift(ctx context.Context) {
	if r.sslCerts == nil || r.serverSettings == nil || r.domains == nil {
		return
	}
	if r.isStandby(ctx) {
		return // DR standby's cert rows are a replica; drsync owns them (GH #331)
	}
	certs, err := r.sslCerts.ListAll(ctx)
	if err != nil {
		r.log.Warn("ssl_san_drift: listing certificates failed", "error", err)
		return
	}
	now := time.Now().UTC()
	attempted := 0
	for _, c := range certs {
		if attempted >= sslSANDriftMaxPerPass {
			break
		}
		if !sslSANDriftEligible(c) {
			continue
		}
		certSANs, err := certDNSNames(*c.CertPath)
		if err != nil {
			continue // unreadable — ssl_observe already surfaces that separately
		}
		// GH #1625: aliases are desired SANs too, so a resolved alias
		// added after issuance is drift the pass must notice. Fetched
		// per eligible cert (hourly, fail-open nil).
		missing := sslSANDriftMissing(c, certSANs, r.aliasHostnames(ctx, c.DomainID))
		if len(missing) == 0 {
			continue // cert already complete (or wildcard) — cheap skip, no IO/LE
		}
		if !r.sanDriftCooldownPassed(c.ID, missing, now) {
			continue
		}
		// Expensive work (DNS + HTTP-01 reachability probes, maybe certbot) begins
		// here. Count it against the per-pass cap and stamp the cooldown up front,
		// so even a cert whose missing SAN turns out unreachable is not re-probed
		// until the cooldown elapses.
		attempted++
		r.markSANDriftAttempt(c.ID, missing, now)
		r.expandCertSANsForDrift(ctx, c, certSANs, missing)
	}
	if attempted > 0 {
		r.log.Info("ssl_san_drift pass", "attempted", attempted)
	}
}

// expandCertSANsForDrift resolves the domain, filters the full desired SAN set
// to what LE could actually validate, confirms that still adds a name the cert
// lacks, and reissues via certbot --expand. Leaves the existing cert untouched
// on any failure.
func (r *Reconciler) expandCertSANsForDrift(ctx context.Context, cert repository.SSLCertificateWithDomain, certSANs, missing []string) {
	dom, err := r.domains.FindByID(ctx, cert.DomainID)
	if err != nil || dom == nil {
		return
	}
	// Re-check the authoritative row: the ListAll snapshot could be stale, and we
	// must never run certbot against a mode the panel doesn't own.
	if dom.SSLMode != models.SSLModeLE && dom.SSLMode != "" {
		return
	}
	// GH #1816: never ask a CA for a certificate on an unproven name.
	if ownershipPending(dom) {
		return
	}

	// GH #2561: a certificate issued over DNS-01 is topped up over DNS-01. The
	// HTTP-01 reachability filter below would hold back a name whose A record
	// points at a CDN — exactly the names DNS-01 validates without trouble —
	// and the HTTP-01 request it makes would be answered at that CDN's edge.
	// With no DNS-01 provider any more, the HTTP-01 top-up runs as before.
	if cert.IssueMethod == issueMethodDNS01 {
		if srv, err := r.settingsGet(ctx); err == nil && srv != nil {
			if route := r.dns01Route(ctx, srv, dom.Name); route.Provider != dns01.ProviderNone {
				r.expandDNS01CertSANs(ctx, dom, cert, srv, route, certSANs, missing)
				return
			}
		}
	}

	desired := sanHostnamesForDomain(dom, r.aliasHostnames(ctx, dom.ID))
	reachable := r.resolvableSANs(ctx, desired)
	if len(reachable) > 0 {
		reachable = r.reachableSANs(ctx, dom.Name, reachable)
	}
	have := make(map[string]struct{}, len(certSANs))
	for _, s := range certSANs {
		have[strings.ToLower(s)] = struct{}{}
	}
	reach := make(map[string]struct{}, len(reachable))
	for _, s := range reachable {
		reach[strings.ToLower(s)] = struct{}{}
	}
	// Build the request as the safe UNION: a desired SAN is requested if it is
	// EITHER already on the cert OR reachable now. certbot's `certonly --expand`
	// replaces the cert's SAN set with exactly the -d list (--expand only
	// suppresses the confirmation prompt), so requesting the raw reachable set
	// would DROP a currently-covered SAN whose HTTP-01 probe flaked this pass —
	// a regression. Keeping already-covered names guarantees we only ever grow.
	var request []string
	addsSomething := false
	for _, d := range desired {
		_, onCert := have[strings.ToLower(d)]
		_, ok := reach[strings.ToLower(d)]
		if onCert || ok {
			request = append(request, d)
		}
		if ok && !onCert {
			addsSomething = true
		}
	}
	// Nothing new is reachable yet (the missing SAN still can't answer HTTP-01):
	// requesting it would just burn LE failed-auth quota. Defer — the cooldown is
	// already stamped, so we retry no sooner than sslSANDriftCooldown from now.
	if !addsSomething {
		r.log.Info("ssl_san_drift: desired SAN not reachable yet, deferring",
			"domain", dom.Name, "missing", strings.Join(missing, ","))
		return
	}

	srv, err := r.settingsGet(ctx)
	if err != nil || srv == nil {
		return
	}

	r.sslIssueMu.Lock()
	defer r.sslIssueMu.Unlock()
	issueCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	params := map[string]any{
		"domain":    dom.Name,
		"webroot":   acmeWebroot(dom),
		"email":     srv.AdminEmail,
		"staging":   r.acmeStaging(cert.Staging),
		"hostnames": request,
	}
	raw, err := r.agent.Call(issueCtx, "ssl.issue", params)
	if err != nil {
		// The current cert keeps serving. Do NOT flip status or self-sign — this
		// is a best-effort SAN top-up, not a cert the domain is waiting on.
		r.log.Warn("ssl_san_drift: expand failed, keeping current cert",
			"domain", dom.Name, "error", firstLine(err.Error()))
		return
	}
	issued, expires, ok := parseSSLIssueResult(raw, r.log, dom.Name)
	if !ok {
		r.log.Warn("ssl_san_drift: unparseable ssl.issue result", "domain", dom.Name)
		return
	}
	var res sslIssueResult
	_ = json.Unmarshal(raw, &res)
	r.recordSANDriftExpand(ctx, dom.Name, cert.ID, issued, expires, res.CertPath, res.KeyPath, request)
}

// expandDNS01CertSANs is the DNS-01 top-up (GH #2561): request the DNS-01 name
// policy — every verified alias whose zone the hook can write, in any zone —
// and reissue over ssl.issue_dns01. Names the policy no longer wants (a
// "www.www." from before) drop off with it. As on the HTTP-01 side, a failure
// leaves the current certificate exactly as it was.
func (r *Reconciler) expandDNS01CertSANs(ctx context.Context, dom *models.Domain, cert repository.SSLCertificateWithDomain, srv *models.ServerSettings, route dns01.Route, certSANs, missing []string) {
	have := lowerSet(certSANs)
	request := r.dns01CertSANs(ctx, srv, dom, route, have, true)
	addsSomething := false
	for _, n := range request {
		if _, ok := have[n]; !ok {
			addsSomething = true
			break
		}
	}
	if !addsSomething {
		r.log.Info("ssl_san_drift: no missing SAN is writable over DNS-01 yet, deferring",
			"domain", dom.Name, "missing", strings.Join(missing, ","))
		return
	}

	r.sslIssueMu.Lock()
	defer r.sslIssueMu.Unlock()
	issueCtx, cancel := context.WithTimeout(ctx, dns01IssueTimeout)
	defer cancel()
	attemptStart := time.Now()
	raw, err := r.agent.Call(issueCtx, "ssl.issue_dns01", map[string]any{
		"cert_name": dom.Name,
		"sans":      request,
		"email":     srv.AdminEmail,
		"staging":   r.acmeStaging(cert.Staging),
	})
	var issued, expires time.Time
	var certPath, keyPath string
	switch {
	case err == nil:
		var ok bool
		if issued, expires, ok = parseSSLIssueResult(raw, r.log, dom.Name); !ok {
			r.log.Warn("ssl_san_drift: unparseable ssl.issue_dns01 result", "domain", dom.Name)
			return
		}
		var res sslIssueResult
		_ = json.Unmarshal(raw, &res)
		certPath, keyPath = res.CertPath, res.KeyPath
	case isAgentDeadlineError(err):
		// JAB-407: certbot runs on after the socket read times out.
		info, ok := r.confirmDNS01IssuedAfterTimeout(dom.Name, request, attemptStart)
		if !ok {
			r.log.Warn("ssl_san_drift: dns-01 expand failed, keeping current cert", "domain", dom.Name, "error", firstLine(err.Error()))
			return
		}
		issued, _ = time.Parse(time.RFC3339, info.NotBefore)
		expires, _ = time.Parse(time.RFC3339, info.NotAfter)
		certPath, keyPath = info.CertPath, info.KeyPath
	default:
		r.log.Warn("ssl_san_drift: dns-01 expand failed, keeping current cert", "domain", dom.Name, "error", firstLine(err.Error()))
		return
	}
	r.recordSANDriftExpand(ctx, dom.Name, cert.ID, issued, expires, certPath, keyPath, request)
}

// recordSANDriftExpand stores an expanded certificate and gets nginx to serve it.
func (r *Reconciler) recordSANDriftExpand(ctx context.Context, domainName, certID string, issued, expires time.Time, certPath, keyPath string, request []string) {
	if err := r.sslCerts.UpdateAfterIssuance(ctx, certID, issued, expires, certPath, keyPath); err != nil {
		r.log.Error("ssl_san_drift: write issuance failed", "domain", domainName, "error", err)
		return
	}
	// The vhost config is unchanged (same cert path), so the per-domain vhost
	// reconcile won't reload — reload nginx explicitly so the freshly-expanded
	// cert is actually served (both the web vhost and the mail vhost reference
	// this file). Best-effort: the cert is on disk regardless.
	reloadCtx, rcancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = r.agent.Call(reloadCtx, "nginx.reload", map[string]any{})
	rcancel()
	r.log.Info("ssl_san_drift: expanded certificate SANs",
		"domain", domainName, "hostnames", strings.Join(request, ","))
}
