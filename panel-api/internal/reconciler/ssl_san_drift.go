package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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
func sslSANDriftMissing(row repository.SSLCertificateWithDomain, certSANs []string) []string {
	dom := &models.Domain{
		Name:          row.DomainName,
		EmailEnabled:  row.EmailEnabled,
		SkipAutoSAN:   row.SkipAutoSAN,
		CreateWWW:     row.CreateWWW,
		MTASTSEnabled: row.MTASTSEnabled,
	}
	desired := sanHostnamesForDomain(dom)
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
	return row.SSLMode == models.SSLModeLE || row.SSLMode == ""
}

// sanDriftCooldownPassed reports whether this cert may be attempted again, and
// (when true) is expected to be paired with markSANDriftAttempt by the caller.
func (r *Reconciler) sanDriftCooldownPassed(certID string, now time.Time) bool {
	r.sanDriftMu.Lock()
	defer r.sanDriftMu.Unlock()
	last, ok := r.sanDriftAttempt[certID]
	return !ok || now.Sub(last) >= sslSANDriftCooldown
}

func (r *Reconciler) markSANDriftAttempt(certID string, now time.Time) {
	r.sanDriftMu.Lock()
	defer r.sanDriftMu.Unlock()
	if r.sanDriftAttempt == nil {
		r.sanDriftAttempt = map[string]time.Time{}
	}
	r.sanDriftAttempt[certID] = now
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
		missing := sslSANDriftMissing(c, certSANs)
		if len(missing) == 0 {
			continue // cert already complete (or wildcard) — cheap skip, no IO/LE
		}
		if !r.sanDriftCooldownPassed(c.ID, now) {
			continue
		}
		// Expensive work (DNS + HTTP-01 reachability probes, maybe certbot) begins
		// here. Count it against the per-pass cap and stamp the cooldown up front,
		// so even a cert whose missing SAN turns out unreachable is not re-probed
		// until the cooldown elapses.
		attempted++
		r.markSANDriftAttempt(c.ID, now)
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

	desired := sanHostnamesForDomain(dom)
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

	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil {
		return
	}

	r.sslIssueMu.Lock()
	defer r.sslIssueMu.Unlock()
	issueCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	params := map[string]any{
		"domain":    dom.Name,
		"webroot":   dom.DocRoot,
		"email":     srv.AdminEmail,
		"staging":   cert.Staging,
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
	if err := r.sslCerts.UpdateAfterIssuance(ctx, cert.ID, issued, expires, res.CertPath, res.KeyPath); err != nil {
		r.log.Error("ssl_san_drift: write issuance failed", "domain", dom.Name, "error", err)
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
		"domain", dom.Name, "hostnames", strings.Join(request, ","))
}
