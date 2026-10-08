// Package reconciler — MTA-STS convergence (M47 Wave 7c, ADR-0109).
//
// Hangs off the per-domain reconcile loop in reconcileDomain (right
// after reconcileSSLForDomain — we need the SSL cert paths). The flow:
//
//	if enabled  && applied_id != mta_sts_id → mail.mtasts.apply, on
//	                                          success stamp applied_id
//	if !enabled && applied_id != 0          → mail.mtasts.disable, on
//	                                          success stamp applied_id=0
//	otherwise                               → no-op
//
// The agent's nginx -t gate is the SAN-coverage check — if the cert
// renewal hasn't yet added mta-sts.<domain>, the apply errors out
// cleanly and applied_id stays stale; next tick retries. Self-healing,
// no DB cert-introspection needed.
//
// "Apply once per id" semantics mean ZERO nginx reloads in the steady
// state — the toggle flips an id once, the apply succeeds, the
// applied_id catches up, and every subsequent reconcile pass exits
// early at the cheap integer compare. No load on nginx, no churn.
package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// mtaStsCallTimeout caps the per-attempt agent call. The agent's
// mail.mtasts.apply writes two small files + a `nginx -t` + reload;
// well under a second on a normal host. 30s leaves room for an apt
// `nginx -t` doing chroot validation on a busy machine without
// stranding the wider reconcile pass behind an unhealthy agent.
const mtaStsCallTimeout = 30 * time.Second

// mtaStsDefaultMaxAge is the policy cache lifetime jabali publishes
// when nothing in the domain row carries an override (none does for
// now — the singleton mode lives in Stalwart's MtaSts config and we
// match its 7-day default).
const mtaStsDefaultMaxAge = 604800

// reconcileMTAStsForDomain converges the agent-side MTA-STS state for
// one domain. Safe to call on every domain pass — it's diff-aware via
// MTASTSAppliedId. Errors log + return: a hung agent must NOT block
// the rest of the reconcile loop.
func (r *Reconciler) reconcileMTAStsForDomain(ctx context.Context, domain *models.Domain) {
	if r.agent == nil || domain == nil {
		return
	}
	switch {
	case domain.MTASTSEnabled && domain.MTASTSAppliedId != domain.MTASTSId:
		r.applyMTASts(ctx, domain)
	case !domain.MTASTSEnabled && domain.MTASTSAppliedId != 0:
		r.disableMTASts(ctx, domain)
	}
}

func (r *Reconciler) applyMTASts(ctx context.Context, domain *models.Domain) {
	if r.sslCerts == nil {
		// No cert repo wired — without cert paths the agent can't
		// write the vhost. Skip and let the next pass try again.
		return
	}
	cert, err := r.sslCerts.FindByDomainID(ctx, domain.ID)
	if err != nil || cert == nil {
		return
	}
	// Cert must be in a state where a file exists on disk. Both
	// "issued" (ACME) and "self_signed" (fallback) qualify — the
	// agent's nginx -t will reject self-signed if a remote MTA's
	// fetch fails, but operator gets the policy visible at all (and
	// the next ACME success re-applies with a real cert).
	if cert.CertPath == nil || cert.KeyPath == nil {
		return
	}
	// The policy mx: must match the domain's published MX host (RFC 8461),
	// which for Jabali-managed mail is mail.<domain> — NOT the panel hostname
	// (GH #441). For external mail providers Jabali does not own the MX, so it
	// must not publish a policy that would mismatch or conflict with the
	// provider's own MTA-STS.
	mxHost, ok := mtaStsMXForDomain(domain)
	if !ok {
		r.log.Debug("mta-sts: skip non-jabali mail provider", "domain", domain.Name, "provider", domain.MailProvider)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, mtaStsCallTimeout)
	defer cancel()
	_, err = r.agent.Call(cctx, "mail.mtasts.apply", map[string]any{
		"domain":   domain.Name,
		"mx_host":  mxHost,
		"mode":     "testing",
		"max_age":  mtaStsDefaultMaxAge,
		"ssl_cert": *cert.CertPath,
		"ssl_key":  *cert.KeyPath,
	})
	if err != nil {
		// Most common transient: cert doesn't cover mta-sts.<domain>
		// yet — nginx -t fails. Log at debug and retry next tick.
		r.log.Debug("mta-sts apply pending", "domain", domain.Name, "err", err)
		return
	}
	if err := r.domains.UpdateMTASTSAppliedID(ctx, domain.ID, domain.MTASTSId); err != nil {
		r.log.Warn("mta-sts applied_id stamp failed", "domain", domain.Name, "err", err)
		return
	}
	r.log.Info("mta-sts: applied", "domain", domain.Name, "id", domain.MTASTSId)
}

// sweepMTAStsSite reports whether site is a domain's MTA-STS vhost
// (`<domain>-mta-sts`, written by mail.mtasts.apply). Such a vhost is not an
// orphan while its domain has a row. When the domain has none and no teardown
// is pending for it, the domain was deleted before domain.delete reaped the
// vhost: it is removed here. Left in place it names the deleted domain's
// certificate, which the teardown removed, so `nginx -t` fails for the whole
// server and nginx doesn't start. The name pattern is only ever written by
// Jabali, so this never reaches an operator's own site.
func (r *Reconciler) sweepMTAStsSite(ctx context.Context, site string, hasRow func(string) bool, pendingTeardowns map[string]bool) bool {
	domain, ok := strings.CutSuffix(site, "-mta-sts")
	if !ok || domain == "" {
		return false
	}
	if hasRow(domain) || pendingTeardowns[domain] {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, mtaStsCallTimeout)
	defer cancel()
	if _, err := r.agent.Call(cctx, "mail.mtasts.disable", map[string]any{"domain": domain}); err != nil {
		r.log.Warn("mta-sts: removing a deleted domain's vhost failed — will retry", "domain", domain, "err", err)
		return true
	}
	r.log.Info("mta-sts: removed a deleted domain's vhost", "domain", domain)
	return true
}

func (r *Reconciler) disableMTASts(ctx context.Context, domain *models.Domain) {
	cctx, cancel := context.WithTimeout(ctx, mtaStsCallTimeout)
	defer cancel()
	if _, err := r.agent.Call(cctx, "mail.mtasts.disable", map[string]any{
		"domain": domain.Name,
	}); err != nil {
		r.log.Warn("mta-sts disable failed", "domain", domain.Name, "err", err)
		return
	}
	if err := r.domains.UpdateMTASTSAppliedID(ctx, domain.ID, 0); err != nil {
		r.log.Warn("mta-sts applied_id clear failed", "domain", domain.Name, "err", err)
		return
	}
	r.log.Info("mta-sts: disabled", "domain", domain.Name)
}

// mtaStsMXForDomain returns the host to publish in the policy's `mx:` line and
// whether MTA-STS should be applied at all (GH #441). RFC 8461 matches the
// policy mx against the MX hostname the receiver resolves. For Jabali-managed
// mail that is mail.<domain> (see dnscompile email/bootstrap records), so the
// policy must use that, not the panel hostname. For external providers
// (m365/google/none) Jabali does not control the MX and must not publish a
// conflicting policy, so applying is skipped.
func mtaStsMXForDomain(d *models.Domain) (string, bool) {
	switch d.MailProvider {
	case "", "jabali":
		return "mail." + d.Name, true
	default:
		return "", false
	}
}
