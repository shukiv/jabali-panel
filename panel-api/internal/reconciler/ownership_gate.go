package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1816 / ADR-0170: a domain whose owner has not proven control of the name
// is converged to its PENDING shape, and the reconciler keeps it there every
// pass. The desired state is "absent", not "skipped", so a domain that goes
// back to pending (admin revoke, a rename to an unproven name) is cleaned up
// by the next pass, and a half-failed cleanup heals on a later one:
//
//   - the zone is not published to authoritative DNS (dns.zone.delete, gated
//     through the same dns.zone ledger entry as the push, so a later
//     verification re-publishes it at once);
//   - the pdns-recursor forward is removed, so no process on this server
//     resolves the name from the tenant's zone;
//   - the web certificate is a self-signed placeholder only (no ACME);
//   - the vhost is rendered with an ownership gate: every request for the
//     real name gets 444 unless the preview URL's proxy sent the gate header;
//   - no mail: no Stalwart recipient, no DKIM2, no mail certificate, no
//     webmail vhost, no MTA-STS vhost, and no relay identity (the cred file
//     is removed and a relay mailbox's password rotated, see
//     retireSendmailCred).
//
// The zone's rows still live in the panel database, so the owner can prepare
// records while waiting; they are published on the pass after verification.

// reconcilePendingDomain is the pending branch of the per-domain passes
// (ReconcileOne and the ReconcileAll domain loop). force re-dispatches the
// vhost past the ledger, as ReconcileOne does for any domain.
func (r *Reconciler) reconcilePendingDomain(ctx context.Context, domain *models.Domain, force bool) {
	dnsCtx, dnsCancel := context.WithTimeout(ctx, 30*time.Second)
	r.reconcileDNSZone(dnsCtx, domain)
	dnsCancel()

	r.reconcileRecursorForwardRemove(ctx, domain.Name)

	// A domain that goes back to pending loses a published MTA-STS policy
	// (its own vhost, with no ownership gate). Clearing the applied id
	// re-applies it after verification.
	if domain.MTASTSAppliedId != 0 && r.agent != nil {
		r.disableMTASts(ctx, domain)
	}

	// A docker-app domain renders a proxy vhost with no preview URL, so a
	// pending one has nothing to serve: take its vhost down.
	if domain.ManagedBy == models.DomainManagedByDockerApp {
		r.removeDockerAppVhost(ctx, domain.Name)
		return
	}
	// The panel's own row (mail-only) and a web-off domain have no vhost.
	if domain.IsPanelPrimary || domain.WebDisabled {
		return
	}

	sslCtx, sslCancel := context.WithTimeout(ctx, 2*time.Minute)
	r.reconcileSSLForDomain(sslCtx, domain)
	sslCancel()

	r.ensureDomainPHPBinding(ctx, domain)
	if force {
		// ReconcileOne's ordering rule: the rate-limit zones must be declared
		// before a vhost that references them. ReconcileAll ran it already.
		r.ReconcileNginxRateLimits(ctx)
	}

	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r.createDomainOnAgent(agentCtx, domain, force)
}

// unpublishPendingZone makes sure a pending domain's zone is not on the
// authoritative server. It shares the dns.zone ledger entry (keyed by the
// zone id) with the push in reconcileDNSZone: the removal is stamped under
// its own fingerprint, so a steady pending domain is re-removed only once
// per audit interval, and the first push after verification is never
// skipped. A server without the DNS module has no zone to remove.
func (r *Reconciler) unpublishPendingZone(ctx context.Context, zone *models.DNSZone) {
	if zone == nil || r.agent == nil {
		return
	}
	name, ok := dnscompile.NormalizeName(zone.Name)
	if !ok {
		return
	}
	_, _ = r.project(ctx, PhaseDNSZone, zone.ID, fingerprint(map[string]any{"unpublished": name}), false, func() error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_, err := r.agent.Call(cctx, "dns.zone.delete", map[string]string{"zone": name})
		if err != nil && strings.Contains(err.Error(), "powerdns backend not available") {
			return nil
		}
		if err != nil {
			r.log.Warn("ownership: unpublish pending zone failed", "zone", name, "err", err)
		}
		return err
	})
}

// ownershipPending reports whether a domain is not proven. Every gate in
// this package asks through here, so a grep finds them all.
func ownershipPending(domain *models.Domain) bool {
	return !domainops.OwnershipVerified(domain)
}

// ownershipGate is the value a pending domain's vhost admits (see
// domainops.PreviewGate).
func ownershipGate(domain *models.Domain) string {
	return domainops.PreviewGate(domain)
}
