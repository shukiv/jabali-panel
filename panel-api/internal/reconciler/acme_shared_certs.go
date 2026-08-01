package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ACME DNS-01 shared-certificate issuance (wildcards).
//
// The HTTP handler only inserts a pending shared_certificates row
// (source='acme', cert_path NULL) — a Let's Encrypt DNS-01 round trip
// takes tens of seconds, which belongs on the reconcile loop, not in an
// open HTTP request. This sweep drives every due row through the agent's
// ssl.issue_dns01, which runs certbot with the panel's acme-hook writing
// the _acme-challenge TXT records through the panel DNS tables.
//
// Renewals: certbot persists the manual hooks in the renewal conf, so
// `certbot renew` (systemd timer) renews the lineage on its own. This
// sweep re-issues as a belt-and-braces path when a cert still nears
// expiry (timer missing or broken) — --keep-until-expiring makes the
// overlap harmless — and refreshes the row's expiry either way.
const (
	// acmeSharedRenewWindow: rows expiring within this window count as due.
	acmeSharedRenewWindow = 30 * 24 * time.Hour
	// acmeSharedRetryBackoff throttles retries of failed first issuance.
	acmeSharedRetryBackoff = 15 * time.Minute
	// acmeSharedGiveUpAfter: once a never-issued row has been failing this
	// long, the cause is structural (hostname zone not publicly delegated,
	// DNS module off) — retrying every 15min forever just burns LE's
	// failed-validation quota. Drop to one attempt per day; a fixed
	// environment succeeds on the next daily try (or immediately after
	// the operator clears last_attempt_at).
	acmeSharedGiveUpAfter = 24 * time.Hour
	// acmeSharedRenewBackoff throttles renewal attempts of issued certs.
	acmeSharedRenewBackoff = 24 * time.Hour
	// acmeSharedIssueTimeout bounds one certbot DNS-01 run: two hook
	// invocations with settle sleeps per name, plus LE's validation poll.
	acmeSharedIssueTimeout = 4 * time.Minute
)

func (r *Reconciler) reconcileAcmeSharedCerts(ctx context.Context) {
	if r.sharedCerts == nil || r.agent == nil || r.serverSettings == nil {
		return
	}
	due, err := r.sharedCerts.ListACMEDue(ctx, time.Now().Add(acmeSharedRenewWindow))
	if err != nil {
		r.log.Error("acme shared certs: list due failed", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil || strings.TrimSpace(srv.AdminEmail) == "" {
		r.log.Warn("acme shared certs: due rows exist but no admin email is set — cannot issue")
		return
	}

	now := time.Now()
	for i := range due {
		cert := &due[i]
		// Backoff. First issuance retries every acmeSharedRetryBackoff;
		// renewals once per acmeSharedRenewBackoff.
		if cert.LastAttemptAt != nil {
			wait := acmeSharedRetryBackoff
			if cert.CertPath != nil {
				wait = acmeSharedRenewBackoff
			} else if now.Sub(cert.CreatedAt) > acmeSharedGiveUpAfter {
				// Still never issued after a day of 15-minute retries:
				// structural failure — daily attempts from here.
				wait = 24 * time.Hour
			}
			if now.Sub(*cert.LastAttemptAt) < wait {
				continue
			}
		}
		var sans []string
		if cert.SANs == nil || json.Unmarshal([]byte(*cert.SANs), &sans) != nil || len(sans) == 0 {
			msg := "row has no usable SAN list"
			_ = r.sharedCerts.MarkACMEAttempt(ctx, cert.ID, &msg)
			r.log.Error("acme shared certs: unusable SANs", "id", cert.ID, "name", cert.Name)
			continue
		}

		issueCtx, cancel := context.WithTimeout(ctx, acmeSharedIssueTimeout)
		raw, err := r.agent.Call(issueCtx, "ssl.issue_dns01", map[string]any{
			"cert_name": cert.ACMELineageName(),
			"sans":      sans,
			"email":     srv.AdminEmail,
			"staging":   cert.Staging,
		})
		cancel()
		if err != nil {
			msg := err.Error()
			if len(msg) > 2048 {
				msg = msg[:2048]
			}
			_ = r.sharedCerts.MarkACMEAttempt(ctx, cert.ID, &msg)
			r.log.Warn("acme shared certs: issue failed", "id", cert.ID, "name", cert.Name, "err", err)
			continue
		}
		var res struct {
			CertPath  string `json:"cert_path"`
			KeyPath   string `json:"key_path"`
			ExpiresAt string `json:"expires_at"`
		}
		if jerr := json.Unmarshal(raw, &res); jerr != nil || res.CertPath == "" {
			msg := "agent returned an unusable ssl.issue_dns01 response"
			_ = r.sharedCerts.MarkACMEAttempt(ctx, cert.ID, &msg)
			r.log.Error("acme shared certs: bad agent response", "id", cert.ID, "err", jerr)
			continue
		}
		expiresAt := time.Now().Add(90 * 24 * time.Hour)
		if t, perr := time.Parse(time.RFC3339, res.ExpiresAt); perr == nil {
			expiresAt = t
		}
		sansJSON := ""
		if cert.SANs != nil {
			sansJSON = *cert.SANs
		}
		if uerr := r.sharedCerts.UpdateACMEIssued(ctx, cert.ID, res.CertPath, res.KeyPath, sansJSON, expiresAt, cert.Staging); uerr != nil {
			r.log.Error("acme shared certs: record issued cert failed", "id", cert.ID, "err", uerr)
			continue
		}
		r.log.Info("acme shared certs: issued", "id", cert.ID, "name", cert.Name, "expires_at", expiresAt.UTC().Format(time.RFC3339))
	}
}
