// panel_primary_dkim.go — auto-provisioning of DKIM + Stalwart domain +
// M6 DNS records for any domain row where email_enabled=1 but no DKIM
// material exists yet.
//
// Why this is a reconciler responsibility: the DB is truth (ADR-0001).
// email_enabled=1 is the default for every new domain (migration 000123),
// so declaring intent in the DB is enough — the reconciler converges the
// on-disk DKIM keypair + Stalwart domain registration + DNS records to
// match. Operators never need to call domain.email_enable explicitly.
//
// Two entry points:
//   - ensurePanelPrimaryDKIM — is_panel_primary=1 rows only (M6.4 / ADR-0048)
//   - ensureTenantEmailEnabled — non-primary tenant domains (migration 000123)

package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// mailOpsDeps builds the shared Domain Mail Lifecycle dependency set
// (internal/domainmailops) for the reconciler's periodic enable convergence.
//
// SSLCerts and SSLReconciler are intentionally omitted. The reconciler
// converges mail SANs through ReconcileSSLSANDrift on its own tick; the
// per-tick enable path has never flipped a cert row, so wiring SSL scheduling
// through Enable here would introduce a cert-row mutation the periodic path
// never performed. JAB-288 requires the extraction to preserve current
// Agent-first behavior, so SSL stays with the drift reconciler.
//
// Call is the agent method value — every caller guards r.agent != nil before
// invoking Enable, so the method value is never built on a nil interface.
func (r *Reconciler) mailOpsDeps() domainmailops.Deps {
	return domainmailops.Deps{
		Domains:        r.domains,
		DNSZones:       r.dnsZones,
		DNSRecords:     r.dnsRecords,
		ServerSettings: r.serverSettings,
		Call:           r.agent.Call,
	}
}

// ensurePanelPrimaryDKIM is a reconciler-scoped mirror of the HTTP
// email-enable handler's EnableDomainEmailInline flow. No-op if DKIM
// is already present (idempotent across reconciler ticks).
//
// Sequence on a row that needs provisioning:
//  1. agent call domain.email_enable → Ed25519 DKIM keypair + Stalwart
//     domain add
//  2. UpdateEmailState on the DB row with selector + public key
//  3. Sync the MX/SPF/DKIM/DMARC DNS records into the self-zone
//
// Errors log and return so the next tick retries — email_enabled stays
// true but DkimSelector stays null, so this same code path fires again.
func (r *Reconciler) ensurePanelPrimaryDKIM(ctx context.Context, domain *models.Domain) {
	if domain == nil || !domain.IsPanelPrimary || !domain.EmailEnabled {
		return
	}
	if !domainIsMailRoutable(domain.Name) {
		r.log.Info("panel-primary DKIM: skipping reserved TLD (RFC 6761)", "domain", domain.Name)
		return
	}
	// Idempotent guard: DKIM already provisioned → nothing to do.
	if domain.DkimSelector != nil && *domain.DkimSelector != "" {
		return
	}
	if r.agent == nil {
		r.log.Warn("panel-primary DKIM: agent unconfigured; skipping", "domain", domain.Name)
		return
	}

	// JAB-286 AC4 / JAB-288 AC5: the periodic enable convergence routes through
	// the shared domainmailops.Enable implementation — Agent provisioning
	// (domain.email_enable → Ed25519 DKIM keypair + Stalwart add), state
	// persistence (UpdateEmailState), and managed-only M6 DNS writes in one
	// declared order — instead of copying the Agent→database→DNS ordering here.
	// Enable validates a complete DKIM response and never persists on an
	// incomplete one, so on error nothing is written and the next tick retries
	// (email_enabled stays 1, DkimSelector stays null). Enable mutates the
	// passed domain in place on success, so subsequent per-tick code on this
	// pass sees the new state without a reload. SSL deps are omitted — see
	// mailOpsDeps.
	selector, _, warnings, err := domainmailops.Enable(ctx, r.mailOpsDeps(), domain)
	if err != nil {
		r.log.Error("panel-primary DKIM: enable convergence failed",
			"domain", domain.Name, "err", err)
		return
	}
	logManagedDNSWarnings(r, "panel-primary DKIM", domain.Name, warnings)

	r.log.Info("panel-primary DKIM: provisioned",
		"domain", domain.Name, "selector", selector)
}

// ensureTenantEmailEnabled is the tenant-domain counterpart of
// ensurePanelPrimaryDKIM. For any non-primary domain that has
// email_enabled=1 but no DKIM material yet, it calls domain.email_enable
// on the agent to generate the Ed25519 keypair, register the Stalwart
// domain, and then syncs the M6 DNS records. No-op once provisioned.
func (r *Reconciler) ensureTenantEmailEnabled(ctx context.Context, domain *models.Domain) {
	if domain == nil || domain.IsPanelPrimary || !domain.EmailEnabled {
		return
	}
	if !domainIsMailRoutable(domain.Name) {
		r.log.Info("tenant email enable: skipping reserved TLD (RFC 6761)", "domain", domain.Name)
		return
	}
	// Idempotent guard: DKIM already provisioned → nothing to do.
	if domain.DkimSelector != nil && *domain.DkimSelector != "" {
		return
	}
	if r.agent == nil {
		r.log.Warn("tenant email enable: agent unconfigured; skipping", "domain", domain.Name)
		return
	}

	// JAB-286 AC4 / JAB-288 AC5: route the periodic enable convergence through
	// the shared domainmailops.Enable (Agent provisioning → state persistence →
	// managed DNS, one declared order; incomplete Agent responses never persist).
	// See ensurePanelPrimaryDKIM and mailOpsDeps for the SSL-nil rationale.
	selector, _, warnings, err := domainmailops.Enable(ctx, r.mailOpsDeps(), domain)
	if err != nil {
		r.log.Error("tenant email enable: enable convergence failed",
			"domain", domain.Name, "err", err)
		return
	}
	logManagedDNSWarnings(r, "tenant email enable", domain.Name, warnings)

	r.log.Info("tenant email enable: provisioned",
		"domain", domain.Name, "selector", selector)
}

// logManagedDNSWarnings emits domainmailops.Enable's best-effort managed-DNS
// warnings at Debug. The pre-extraction reconciler logged a user-edited M6
// record conflict at Debug deliberately — it is the expected steady state and
// would otherwise spam ~60 lines/hr per edited record. Enable returns that same
// conflict as a warning string (no internal log), so logging at Debug preserves
// the anti-spam level; genuine hard failures (missing zone, create error) are
// already surfaced at Error from inside domainmailops.SyncManagedDNSOnEnable.
func logManagedDNSWarnings(r *Reconciler, prefix, domainName string, warnings []string) {
	for _, w := range warnings {
		r.log.Debug(prefix+": managed-DNS warning", "domain", domainName, "warning", w)
	}
}

// ensureTenantDKIMRecords back-fills the M6 DNS records (jabali._domainkey
// TXT, autoconfig CNAME, _autodiscover._tcp SRV) for any non-panel-primary
// tenant domain whose DB row has DKIM material but whose dns_records table
// is missing those rows. Symptom this fixes: a domain enabled before the
// M6 DNS-emit code path landed (or one whose insert failed mid-flight) has
// `email_enabled=1` + a populated `dkim_public_key`, yet the DNS Records
// page shows no `_domainkey` TXT — outbound mail signs but receivers can't
// verify because the public key isn't published.
//
// Idempotent — `syncPanelPrimaryEmailDNS` already skips rows that already
// exist (managed_by=m6) or conflict (user-edited). Reuses that helper
// despite the name; M6.4 (panel-primary) and tenant flows are functionally
// identical for the DNS-sync step.
func (r *Reconciler) ensureTenantDKIMRecords(ctx context.Context, domain *models.Domain) {
	if domain == nil || domain.IsPanelPrimary || !domain.EmailEnabled {
		return
	}
	if domain.DkimSelector == nil || *domain.DkimSelector == "" {
		return
	}
	if domain.DkimPublicKey == nil || *domain.DkimPublicKey == "" {
		return
	}
	r.syncPanelPrimaryEmailDNS(ctx, domain.ID, *domain.DkimSelector, *domain.DkimPublicKey)
}

// syncPanelPrimaryEmailDNS mirrors syncEmailDNSOnEnableInline in the API
// package without the cross-package import.
func (r *Reconciler) syncPanelPrimaryEmailDNS(ctx context.Context, domainID, selector, pubKey string) {
	if r.dnsZones == nil || r.dnsRecords == nil {
		return
	}
	zone, err := r.dnsZones.FindByDomainID(ctx, domainID)
	if err != nil {
		r.log.Warn("panel-primary DKIM: DNS zone lookup failed",
			"domain_id", domainID, "err", err)
		return
	}
	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Warn("panel-primary DKIM: DNS record list failed",
			"zone_id", zone.ID, "err", err)
		return
	}
	var srv *models.ServerSettings
	if r.serverSettings != nil {
		srv, _ = r.serverSettings.Get(ctx)
	}
	intended := dnscompile.BuildEmailRecords(zone.ID, zone.Name, selector, pubKey, srv, ids.NewULID, time.Now().UTC())
	for i := range intended {
		rec := intended[i]
		if hasExistingM6DNSRecord(existing, rec.Name, rec.Type) {
			continue
		}
		if conflictingDNSRecord(existing, rec.Name, rec.Type) {
			// Demoted from Warn to Debug: this is the EXPECTED steady
			// state after a user/admin edits an M6 record (managed_by
			// flips to NULL, our reconciler should NOT stomp). Fires every
			// tick = 60 noise lines/hr per edited record. Real "conflict"
			// against an outside-edit-we-can't-explain hasn't happened in
			// practice; if it ever does, the operator can spot via
			// `journalctl -u jabali-panel -p debug --grep "DNS record
			// conflict"` after enabling debug.
			r.log.Debug("panel-primary DKIM: DNS record conflict; leaving existing user record in place",
				"zone_id", zone.ID, "name", rec.Name, "type", rec.Type)
			continue
		}
		if err := r.dnsRecords.Create(ctx, &rec); err != nil {
			r.log.Warn("panel-primary DKIM: DNS record create failed",
				"zone_id", zone.ID, "name", rec.Name, "type", rec.Type, "err", err)
		}
	}
}

// hasExistingM6DNSRecord reports whether any previously-inserted M6 record
// already matches the (name, type) tuple. Matches `managed_by = "m6"` so
// non-M6 rows are never considered "already present" — which is what we
// want for conflict detection on the next rule below.
func hasExistingM6DNSRecord(existing []models.DNSRecord, name, recType string) bool {
	for _, r := range existing {
		if r.Name == name && r.Type == recType && r.ManagedBy != nil && *r.ManagedBy == "m6" {
			return true
		}
	}
	return false
}

// domainIsMailRoutable returns false for RFC 6761 reserved TLDs
// (.local, .test, .localhost, .invalid, .example) that Stalwart refuses
// with invalidPatch on Domain/set.
func domainIsMailRoutable(hostname string) bool {
	host := strings.ToLower(strings.TrimRight(strings.TrimSpace(hostname), "."))
	if host == "" {
		return false
	}
	lastDot := strings.LastIndex(host, ".")
	var tld string
	if lastDot < 0 {
		tld = host
	} else {
		tld = host[lastDot+1:]
	}
	switch tld {
	case "local", "localhost", "invalid", "test", "example":
		return false
	}
	return true
}

// conflictingDNSRecord reports whether a non-M6 row is already at
// (name, type) — we leave those alone so the user's custom DNS wins.
func conflictingDNSRecord(existing []models.DNSRecord, name, recType string) bool {
	for _, r := range existing {
		if r.Name == name && r.Type == recType {
			if r.ManagedBy == nil || *r.ManagedBy != "m6" {
				return true
			}
		}
	}
	return false
}
