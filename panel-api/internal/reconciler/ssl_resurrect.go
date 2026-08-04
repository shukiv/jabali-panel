package reconciler

import (
	"context"
	"time"
)

// ssl_resurrect.go — JAB-224.
//
// A certificate that exhausts its ACME attempts is parked at status='failed',
// and ListDueForACMERetry deliberately skips those: "operator-only (manual reset
// to 'pending')". That is the right call for a certificate that failed because
// something is broken — retrying a misconfiguration forever just burns Let's
// Encrypt quota.
//
// It is the wrong call for the case this panel creates constantly. A migrated
// domain cannot pass HTTP-01 until its DNS points at this host, so before
// cutover it fails EVERY attempt, by definition. It therefore always exhausts
// its budget and parks at 'failed' — and then nothing retries it at the one
// moment issuance would finally succeed: the cutover itself.
//
// The result is the 526: the domain goes live still serving jabali's self-signed
// placeholder, and Cloudflare Full (strict) refuses it. The operator sees a
// certificate that "failed" hours ago and no indication that it would work now.
//
// So: re-arm exhausted certificates on domains where SSL is still wanted, slowly
// and in small increments, so cutover is picked up automatically without the
// panel hammering the ACME endpoint for domains that will keep failing.

const (
	// sslResurrectAfter is how long an exhausted certificate rests before it is
	// re-armed. It bounds the worst-case window between cutover and issuance.
	sslResurrectAfter = 1 * time.Hour

	// sslResurrectAttempts is how many attempts each re-arm buys. Let's Encrypt
	// allows 5 failed validations per hostname per hour; granting 2 per hour
	// stays well under that even when every domain in a migration batch is
	// still pre-cutover and failing together.
	sslResurrectAttempts = 2

	// sslResurrectMaxRetryCount mirrors the giving-up threshold in the ACME
	// retry path (20 failures -> failed). Re-arming sets the counter near that
	// ceiling rather than zeroing it, so a re-armed certificate gets a couple of
	// tries and parks again instead of receiving a full fresh budget every hour.
	sslResurrectMaxRetryCount = 20

	// sslResurrectBatch caps how many are re-armed per pass, so one tick cannot
	// queue a whole fleet's worth of ACME work at once.
	sslResurrectBatch = 25
)

// ReconcileSSLResurrect re-arms certificates that gave up while SSL is still
// enabled on the domain, so a post-cutover retry happens without operator
// action.
//
// Best-effort and deliberately quiet when there is nothing to do: on a settled
// host this finds zero rows and logs nothing.
func (r *Reconciler) ReconcileSSLResurrect(ctx context.Context) {
	if r.sslCerts == nil {
		return
	}
	now := time.Now().UTC()
	certs, err := r.sslCerts.ListExhaustedForSSLEnabledDomains(ctx, now.Add(-sslResurrectAfter), sslResurrectBatch)
	if err != nil {
		r.log.Warn("ssl_resurrect: listing exhausted certificates failed", "error", err)
		return
	}
	if len(certs) == 0 {
		return
	}

	rearmed := 0
	for _, c := range certs {
		if err := r.sslCerts.RearmACME(ctx, c.ID, sslResurrectMaxRetryCount-sslResurrectAttempts, now); err != nil {
			r.log.Warn("ssl_resurrect: re-arm failed", "cert_id", c.ID, "error", err)
			continue
		}
		rearmed++
	}
	if rearmed > 0 {
		r.log.Info("ssl_resurrect: re-armed exhausted certificates for another attempt",
			"count", rearmed,
			"attempts_each", sslResurrectAttempts,
			"reason", "a pre-cutover domain fails ACME every time and would otherwise never retry after cutover")
	}
}
