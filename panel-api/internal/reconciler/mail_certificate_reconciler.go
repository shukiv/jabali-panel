package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileMailCertificates is the M6.6 reconciler hook. For every
// domain that has opted in to per-domain mail TLS (mail_certificate
// row exists), drive the state machine:
//
//   pending      → dispatch ssl.mail.issue immediately.
//   dns_missing  → re-check at most every hour.
//   failed       → 3h backoff (next_retry_at gate) then retry.
//   issuing      → no-op; the in-flight call writes the terminal
//                  state. Watchdog timeout in step 5b if added later.
//   issued       → renew when <30d to expiry.
//   disabled     → operator opted out; skip.
//
// Per-tenant LE rate-limit guard lives in step 7.
func (r *Reconciler) reconcileMailCertificates(ctx context.Context) {
	if r.mailCerts == nil || r.serverSettings == nil {
		return
	}
	// Backfill: every email-enabled domain must have a mail_certificate
	// row so the state machine below has something to drive. Without
	// this pass, pre-M6.6 domains stay self-signed forever — they were
	// never given a row at create time (M6.6 shipped after they were
	// created) and the on-demand REST handler (GET .../mail-tls) only
	// fires when an admin opens the Mail TLS section in the UI. Caught
	// by GH #132: fresh mail.<d>:465 served the Stalwart self-signed
	// default cert because no row was ever lazy-created.
	if r.domains != nil {
		domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
		if err != nil {
			r.log.Warn("mail-cert backfill failed to list domains", "error", err)
		} else {
			for i := range domains {
				d := &domains[i]
				if !d.EmailEnabled {
					continue
				}
				if _, err := r.mailCerts.EnsureForDomain(ctx, d.ID); err != nil {
					r.log.Warn("mail-cert backfill EnsureForDomain failed", "domain_id", d.ID, "error", err)
				}
			}
		}
	}
	rows, err := r.mailCerts.List(ctx)
	if err != nil {
		r.log.Warn("mail-cert reconcile failed to list", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	settings, err := r.serverSettings.Get(ctx)
	if err != nil {
		r.log.Debug("mail-cert reconcile skipped: server_settings unavailable", "error", err)
		return
	}
	if settings.AdminEmail == "" || settings.PublicIPv4 == "" {
		r.log.Debug("mail-cert reconcile skipped: admin_email or public_ipv4 empty")
		return
	}

	for _, row := range rows {
		if row == nil {
			continue
		}
		r.reconcileOneMailCert(ctx, row, settings.AdminEmail, settings.PublicIPv4)
	}
}

// mailCertRenewWindow: renew when <30d to expiry. Matches the panel-
// cert reconciler's window so operators only have to remember one
// number.
const mailCertRenewWindow = 30 * 24 * time.Hour

// mailCertNewOrderSoftCap — gate the reconciler from dispatching
// first-issuance attempts past this number in any rolling 7d window.
// LE's hard limit is 50 new orders / week / registered domain; 40
// leaves headroom for the panel hostname cert (M32) + AppSec etc on
// the same registered domain. Renewals are exempt and don't count.
//
// Hitting the soft cap parks every NEW issuance attempt in
// dns_missing-style backoff with a clear last_error; renewals
// proceed unaffected. Operator can raise the cap by editing this
// constant when they've requested an LE rate-limit increase.
const mailCertNewOrderSoftCap = 40

func (r *Reconciler) reconcileOneMailCert(ctx context.Context, row *models.MailCertificate, adminEmail, publicIPv4 string) {
	switch row.Status {
	case models.MailCertStatusDisabled:
		return
	case models.MailCertStatusIssuing:
		// Verb is in flight from a previous tick; let it land.
		return
	case models.MailCertStatusDNSMissing, models.MailCertStatusFailed:
		if row.NextRetryAt != nil && row.NextRetryAt.After(time.Now()) {
			return
		}
	case models.MailCertStatusIssued:
		// Renewal gate.
		if row.ExpiresAt == nil || time.Until(*row.ExpiresAt) > mailCertRenewWindow {
			return
		}
	case models.MailCertStatusPending:
		// Fall through.
	default:
		return
	}

	domainName, ok := r.lookupDomainName(ctx, row.DomainID)
	if !ok {
		r.log.Debug("mail-cert reconcile: domain row gone", "id", row.ID, "domain_id", row.DomainID)
		return
	}

	// Rate-limit guard. Only counts FIRST-issuance attempts; existing
	// renewals (row.IssuedAt set) are exempt because LE doesn't count
	// them against new-order quota.
	isFirstIssue := row.IssuedAt == nil
	if isFirstIssue {
		count, cerr := r.mailCerts.CountFirstIssueAttemptsLastWeek(ctx)
		if cerr != nil {
			r.log.Warn("mail-cert rate-limit count failed", "error", cerr)
		} else if count >= mailCertNewOrderSoftCap {
			msg := "deferred: panel hit Let's Encrypt new-order soft cap (40/week). Will retry next week or after operator clears."
			_ = r.mailCerts.MarkFailed(ctx, row.ID, msg, 24*time.Hour)
			r.log.Warn("mail-cert rate-limit cap reached", "count", count, "cap", mailCertNewOrderSoftCap, "id", row.ID)
			return
		}
	}

	// Flip to issuing BEFORE the verb so a concurrent tick doesn't
	// dispatch twice. WithoutCancel-style isn't needed here because
	// the reconciler owns the context.
	if err := r.mailCerts.UpdateStatus(ctx, row.ID, models.MailCertStatusIssuing, nil); err != nil {
		r.log.Warn("mail-cert reconcile failed to flip issuing", "id", row.ID, "error", err)
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	raw, err := r.agent.Call(callCtx, "ssl.mail.issue", map[string]any{
		"domain_id": row.DomainID,
		"domain":    domainName,
		"email":     adminEmail,
		"public_ip": publicIPv4,
		"webroot":   "/var/www/jabali-acme",
	})
	if err != nil {
		errMsg := firstLineString(err.Error())
		_ = r.mailCerts.MarkFailed(ctx, row.ID, errMsg, 3*time.Hour)
		r.log.Warn("mail-cert ssl.mail.issue dispatch failed", "id", row.ID, "domain", domainName, "error", err)
		return
	}
	var resp struct {
		Outcome     string    `json:"outcome"`
		LineagePath string    `json:"lineage_path"`
		IssuedAt    string    `json:"issued_at"`
		ExpiresAt   string    `json:"expires_at"`
		Detail      string    `json:"detail"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		_ = r.mailCerts.MarkFailed(ctx, row.ID, "parse ssl.mail.issue response: "+err.Error(), 3*time.Hour)
		r.log.Warn("mail-cert parse failed", "id", row.ID, "error", err)
		return
	}

	switch resp.Outcome {
	case "issued":
		issuedAt, _ := time.Parse(time.RFC3339, resp.IssuedAt)
		expiresAt, _ := time.Parse(time.RFC3339, resp.ExpiresAt)
		if err := r.mailCerts.MarkIssued(ctx, row.ID, resp.LineagePath, issuedAt, expiresAt); err != nil {
			r.log.Warn("mail-cert MarkIssued failed", "id", row.ID, "error", err)
			return
		}
		r.log.Info("mail-cert issued", "id", row.ID, "domain", domainName, "expires_at", expiresAt)
	case "dns_missing":
		detail := resp.Detail
		if detail == "" {
			detail = "one or more SAN hostnames do not resolve to the panel IP"
		}
		_ = r.mailCerts.MarkDNSMissing(ctx, row.ID, detail)
		r.log.Info("mail-cert dns missing", "id", row.ID, "domain", domainName, "detail", detail)
	default:
		// "failed" + anything unexpected.
		detail := resp.Detail
		if detail == "" {
			detail = "ssl.mail.issue returned outcome=" + resp.Outcome
		}
		_ = r.mailCerts.MarkFailed(ctx, row.ID, detail, 3*time.Hour)
		r.log.Warn("mail-cert failed", "id", row.ID, "domain", domainName, "detail", detail)
	}
}

// lookupDomainName resolves the domains row -> Name string. Wraps
// r.domains.Get so the reconciler doesn't sprinkle nil checks on the
// repo wire-in for callers that don't set it (test fixtures).
func (r *Reconciler) lookupDomainName(ctx context.Context, domainID string) (string, bool) {
	if r.domains == nil {
		return "", false
	}
	d, err := r.domains.FindByID(ctx, domainID)
	if err != nil || d == nil {
		return "", false
	}
	name := strings.TrimSpace(strings.ToLower(d.Name))
	if name == "" {
		return "", false
	}
	return name, true
}
