package reconciler

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ssl_observe.go — JAB-203. Reconcile the panel's view of a certificate with
// the certificate actually on disk.
//
// OWNERSHIP (ADR-0163): certbot's own systemd timer renews certificates. The
// panel does NOT drive renewal — StartSSLTicker and ListDueForRenewal, which
// would have, were dead code with zero call sites for months and are deleted.
// This pass is the other half of that decision: if something outside the panel
// renews, the panel has to OBSERVE rather than assume.
//
// Two bugs follow from not observing, both seen on a live box:
//
//   - expires_at drifts. Measured on testserver: panel said 2026-08-15 while
//     the certificate on disk ran to 2026-10-30 — 2.5 months stale, because
//     nothing refreshed the row after certbot renewed. eventsources/cert_renew.go
//     computes domain.expiry.7d / 1d from that column, so those alerts fire off
//     a value nothing keeps current, in either direction.
//
//   - cert.renew.fail cannot fire for the failure that actually happens. It
//     triggers on status=failed && last_error != nil, which only a panel-driven
//     ACME attempt ever sets. A certbot-timer failure leaves the row untouched:
//     testserver sat at status=issued, last_error=NULL, retry_count=0 through
//     daily renewal failures. The kind is DefaultOn and advertises "action
//     usually required", and it could not fire.
//
// So the signal has to come from observation: a certificate whose on-disk
// notAfter is inside the renewal window and is NOT moving is a renewal that is
// silently failing, regardless of who was supposed to renew it.

// sslRenewalWindow is how long before expiry a certificate should already have
// been renewed. Let's Encrypt issues 90-day certificates and certbot renews at
// 30 days remaining, so a cert still inside 21 days has missed at least a week
// of daily timer runs — comfortably past "certbot will get to it".
const sslRenewalWindow = 21 * 24 * time.Hour

// sslObserveResult is what one pass saw. Returned for logging and tests rather
// than kept, so the pass stays stateless.
type sslObserveResult struct {
	Checked   int
	Refreshed int      // rows whose expires_at disagreed with disk and were corrected
	Overdue   []string // domains inside the renewal window and not renewing
	// Unreadable keeps "I could not look" separate from both "it is fine" and
	// "it failed to renew". A permissions change must not page an operator
	// about a healthy certificate, and must not silently pass either.
	Unreadable []string
}

// certNotAfter parses a PEM certificate file and returns the LEAF's notAfter.
//
// fullchain.pem holds the leaf followed by the issuer chain. Taking the first
// block is deliberate — the intermediate outlives the leaf by years, so
// grabbing the wrong one would report a cert as healthy long after it expired.
func certNotAfter(path string) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, fmt.Errorf("%s: no PEM CERTIFICATE block", path)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", path, err)
	}
	return c.NotAfter, nil
}

// certDNSNames parses a PEM certificate file and returns the LEAF's SAN DNS
// names, lower-cased. Used by the SAN-drift pass (JAB-226) to compare what a
// cert actually covers against what the domain's flags say it should. Takes the
// first PEM block for the same reason as certNotAfter — the leaf, not the chain.
func certDNSNames(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no PEM CERTIFICATE block", path)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]string, 0, len(c.DNSNames))
	for _, n := range c.DNSNames {
		out = append(out, strings.ToLower(n))
	}
	return out, nil
}

// observeSSLCerts compares each issued certificate's row against its file and
// reports what it found. Pure apart from reading the files: the caller does the
// database writes and the alerting, so the comparison is testable on its own.
func observeSSLCerts(certs []repository.SSLCertificateWithDomain, now time.Time) (sslObserveResult, map[string]time.Time) {
	var res sslObserveResult
	refresh := map[string]time.Time{} // cert ID -> notAfter from disk

	for _, c := range certs {
		if c.Status != models.SSLStatusIssued || c.CertPath == nil || *c.CertPath == "" {
			continue
		}
		res.Checked++
		notAfter, err := certNotAfter(*c.CertPath)
		if err != nil {
			res.Unreadable = append(res.Unreadable, fmt.Sprintf("%s: %v", c.DomainName, err))
			continue
		}
		// Disagreement in EITHER direction is drift worth correcting: a row
		// ahead of disk hides an expiry, a row behind it fires false alarms.
		if c.ExpiresAt == nil || !c.ExpiresAt.Equal(notAfter) {
			refresh[c.ID] = notAfter
			res.Refreshed++
		}
		// Overdue is judged on the FILE, never on the row — the row is the
		// thing we do not trust.
		if notAfter.Sub(now) < sslRenewalWindow {
			res.Overdue = append(res.Overdue, c.DomainName)
		}
	}
	return res, refresh
}

// ReconcileSSLObservation refreshes expires_at/last_renewed_at from disk and
// reports certificates that are overdue for renewal.
//
// Called from the reconciler tick. Best-effort by design: a certificate whose
// file cannot be read is recorded as unreadable and skipped, never treated as
// expired — a permissions change must not page an operator about a healthy
// certificate.
func (r *Reconciler) ReconcileSSLObservation(ctx context.Context) {
	if r.sslCerts == nil {
		return
	}
	if r.isStandby(ctx) {
		return // DR standby's cert rows are a replica; drsync owns them (GH #331 Step 3)
	}
	certs, err := r.sslCerts.ListAll(ctx)
	if err != nil {
		r.log.Warn("ssl_observe: listing certificates failed", "error", err)
		return
	}
	now := time.Now().UTC()
	res, refresh := observeSSLCerts(certs, now)

	for id, notAfter := range refresh {
		if err := r.sslCerts.RefreshObservedExpiry(ctx, id, notAfter, now); err != nil {
			r.log.Warn("ssl_observe: refreshing expiry failed", "cert_id", id, "error", err)
		}
	}
	if res.Refreshed > 0 || len(res.Overdue) > 0 || len(res.Unreadable) > 0 {
		r.log.Info("ssl_observe pass",
			"checked", res.Checked,
			"refreshed", res.Refreshed,
			"overdue", len(res.Overdue),
			"unreadable", len(res.Unreadable))
	}
	for _, u := range res.Unreadable {
		r.log.Warn("ssl_observe: certificate file unreadable — not treated as expired", "detail", u)
	}
	// The alert the issue asked for: fired from what the certificate ON DISK
	// says, not from a panel-driven attempt that may never happen.
	for _, dom := range res.Overdue {
		r.publishCertRenewOverdue(ctx, dom)
	}
}

// publishCertRenewOverdue fires cert.renew.fail from OBSERVED state.
//
// The existing publisher fires on status=failed && last_error != nil, which
// only a panel-driven ACME attempt sets — so for a certbot-timer failure, the
// most likely failure mode, it never fired at all. This one asks a different
// question: is the certificate on disk inside its renewal window and still not
// renewed? That is true regardless of who was supposed to renew it, which is
// the point.
func (r *Reconciler) publishCertRenewOverdue(ctx context.Context, domain string) {
	if r.notificationQueue == nil {
		return
	}
	if _, err := r.notificationQueue.Publish(ctx, notifications.Envelope{
		EventKind: "cert.renew.fail",
		Severity:  "error",
		Title:     "Certificate not renewing: " + domain,
		Body: "The certificate for " + domain + " is inside its renewal window and has " +
			"not been renewed.\n\nRenewal is handled by certbot's systemd timer, so the panel " +
			"cannot retry it directly. Check `systemctl status certbot.service` and " +
			"`certbot renew --dry-run` on this server.\n\nThis was detected by reading the " +
			"certificate file itself, not from a panel-driven renewal attempt.",
		Deeplink: "/jabali-admin/ssl",
	}); err != nil {
		r.log.Warn("ssl_observe: publishing cert.renew.fail failed", "domain", domain, "error", err)
	}
}
