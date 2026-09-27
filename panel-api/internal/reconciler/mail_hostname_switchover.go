package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailhostops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// JAB-390 switchover timings.
const (
	// An issuing attempt not updated for this long died with its process
	// and may be claimed again. It matches the panel-cert pass's stale
	// pending_acme rule.
	mailHostSwitchoverStaleAfter = 10 * time.Minute
	// A name that does not point at this server, or a panel that is not
	// ready, is re-checked soon: the admin is likely fixing it right now.
	mailHostSwitchoverNotReadyRetry = 10 * time.Minute
	// A failed issue is not retried every tick (Let's Encrypt rate limits).
	mailHostSwitchoverIssueRetry   = time.Hour
	mailHostSwitchoverIssueTimeout = 3 * time.Minute
	// mailHostSwitchoverServedTimeout bounds ssl.panel.mail_served, which
	// polls each mail port for up to 30 seconds while Stalwart restarts.
	mailHostSwitchoverServedTimeout = 2 * time.Minute
)

// reconcileMailHostnameSwitchover moves the shared panel mail hostname
// (JAB-390) and keeps Bulwark's JMAP URL and the /webmail redirects on the
// effective one.
//
// A request (mail_hostname_switchover.desired) is applied only after the
// requested name — and mail.<hostname>, which stays served — point at this
// server and one certificate for both is issued and deployed to Stalwart
// and panel-mail.crt. Complete then, in one transaction, applies the name
// (server_settings.mail_hostname), moves the mail certificate row to it and
// marks the request done. Every failure leaves the applied name alone and
// records the reason on the request.
//
// Before that, a panel renamed with nothing applied gets the name its issued
// mail certificate serves pinned as the applied one (pinIssuedMailHostname).
//
// It runs right after the panel-cert pass. The run's settings snapshot is
// not refreshed after a switchover, so later passes in the same tick (the
// webmail vhost sweep) still see the old name; their fingerprints change
// with the name, so the next tick converges them.
func (r *Reconciler) reconcileMailHostnameSwitchover(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil {
		return
	}
	settings, err := r.settingsGet(ctx)
	if err != nil || settings == nil || settings.Hostname == "" {
		return
	}
	r.pinIssuedMailHostname(ctx, settings)
	effective := models.EffectiveMailHostname(settings.MailHostname, settings.Hostname)
	if applied, ok := r.runMailHostnameSwitchover(ctx, settings); ok {
		effective = applied
	}
	r.assertWebmailJMAPURL(ctx, effective)
	r.assertWebmailRedirect(ctx, effective)
}

// pinIssuedMailHostname keeps the mail hostname on the name the issued
// panel mail certificate serves when the panel is renamed.
//
// JAB-389 keeps that certificate, and its row, on the name it was issued for
// across a rename. With no applied mail hostname the effective one would
// follow the new panel hostname, so Bulwark and the webmail vhosts would move
// to mail.<new-hostname>, a name no certificate covers and whose DNS may not
// exist. When nothing is applied and an issued Let's Encrypt mail certificate
// serves a valid name other than mail.<hostname>, that name is recorded as
// the applied one. Moving mail to mail.<new-hostname> is then a switchover
// request like any other.
//
// A self-signed mail certificate is not pinned: it is regenerated for the
// current hostname, so its row name is stale by design. On success, or when
// another writer applied a name first, s is updated and the run's settings
// snapshot dropped, so later passes in this tick see the stored name.
func (r *Reconciler) pinIssuedMailHostname(ctx context.Context, s *models.ServerSettings) {
	if r.mailHostSwitchover == nil || r.panelCerts == nil || s.MailHostname != nil {
		return
	}
	row, err := r.panelCerts.GetByKind(ctx, models.PanelCertKindMail)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			r.log.Warn("mail hostname pin: load mail cert row", "error", err)
		}
		return
	}
	if !row.UseLE || row.Status != models.PanelCertStatusIssued {
		return
	}
	name, err := models.ValidateMailHostname(row.Hostname)
	if err != nil || name == models.PanelMailHostname(s.Hostname) || strings.EqualFold(name, s.Hostname) {
		return
	}
	wrote, err := r.mailHostSwitchover.PinApplied(ctx, name)
	if err != nil {
		r.log.Warn("mail hostname pin failed", "mail_hostname", name, "error", err)
		return
	}
	r.settingsForget(ctx)
	if !wrote {
		// Another writer applied a name first; use what is stored.
		if fresh, ferr := r.settingsGet(ctx); ferr == nil && fresh != nil {
			s.MailHostname = fresh.MailHostname
		}
		return
	}
	s.MailHostname = &name
	r.log.Info("mail hostname pinned to the issued mail certificate after a panel rename",
		"mail_hostname", name, "panel_hostname", s.Hostname)
}

// runMailHostnameSwitchover runs one due switchover attempt. It returns the
// newly effective mail hostname and true when the switchover completed.
func (r *Reconciler) runMailHostnameSwitchover(ctx context.Context, s *models.ServerSettings) (string, bool) {
	if r.mailHostSwitchover == nil || r.panelCerts == nil || r.panelCertRoutability == nil || r.domains == nil {
		return "", false
	}
	sw, err := r.mailHostSwitchover.Get(ctx)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			r.log.Warn("mail hostname switchover: load request", "error", err)
		}
		return "", false
	}
	if sw.Desired == nil || !mailHostSwitchoverDue(sw, time.Now()) {
		return "", false
	}
	desired := *sw.Desired

	hostRow, err := r.panelCerts.GetByKind(ctx, models.PanelCertKindHostname)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		r.log.Warn("mail hostname switchover: load hostname cert row", "error", err)
		return "", false
	}
	mailRow, err := r.panelCerts.GetByKind(ctx, models.PanelCertKindMail)
	if err != nil {
		r.log.Warn("mail hostname switchover: load mail cert row", "error", err)
		return "", false
	}
	primary, err := r.domains.FindPanelPrimary(ctx)
	if err != nil && !errors.Is(err, repository.ErrPanelPrimaryNotFound) {
		r.log.Warn("mail hostname switchover: load panel-primary domain", "error", err)
		return "", false
	}
	if err := mailhostops.Ready(s, hostRow, primary); err != nil {
		r.failMailHostnameSwitchover(ctx, desired, err.Error(), mailHostSwitchoverNotReadyRetry)
		return "", false
	}
	// A tenant can create a domain (or alias) after the request was made;
	// never issue for a name a tenant answers or whose zone it controls.
	if err := r.checkMailHostname(ctx, s, desired); err != nil {
		r.failMailHostnameSwitchover(ctx, desired, err.Error(), mailHostSwitchoverNotReadyRetry)
		return "", false
	}
	// An issue of the mail certificate may be in flight (the admin's
	// "issue now"); wait for it, as the panel-cert pass does.
	if mailRow.Status == models.PanelCertStatusPendingACME && time.Since(mailRow.UpdatedAt) < mailHostSwitchoverStaleAfter {
		return "", false
	}

	// The certificate covers the old derived name too, so mail clients
	// still configured with it keep working (dual-serve). Both names must
	// point here, or certbot fails the whole certificate; check first so
	// the reason names the name at fault.
	derived := models.PanelMailHostname(s.Hostname)
	extra := []string{}
	if desired != derived {
		extra = append(extra, derived)
	}
	for _, name := range append([]string{desired}, extra...) {
		gate, err := r.panelCertRoutability.Check(ctx, name, s.PublicIPv4, true)
		if err != nil {
			r.log.Warn("mail hostname switchover: routability check", "name", name, "error", err)
			return "", false
		}
		if !gate.Routable {
			r.failMailHostnameSwitchover(ctx, desired,
				fmt.Sprintf("%s does not point at this server (%s)", name, gate.Reason), mailHostSwitchoverNotReadyRetry)
			return "", false
		}
	}

	now := time.Now()
	claimed, err := r.mailHostSwitchover.Claim(ctx, desired, now.Add(-mailHostSwitchoverStaleAfter), now)
	if err != nil {
		r.log.Warn("mail hostname switchover: claim", "desired", desired, "error", err)
		return "", false
	}
	if !claimed {
		return "", false
	}

	r.sslIssueMu.Lock()
	defer r.sslIssueMu.Unlock()
	certPath := mailRow.CertPEMPath
	if certPath == "" {
		certPath = "/etc/jabali/tls/panel-mail.crt"
	}
	callCtx, cancel := context.WithTimeout(ctx, mailHostSwitchoverIssueTimeout)
	defer cancel()
	// force_deploy: the lineage may already hold a valid certificate
	// (switching back to mail.<hostname>, or a retry after an interrupted
	// deploy); certbot keeps it, and it must still reach Stalwart and the
	// lineage record.
	raw, err := r.agent.Call(callCtx, "ssl.panel.issue", map[string]any{
		"hostname":        desired,
		"extra_hostnames": extra,
		"email":           s.AdminEmail,
		"staging":         mailRow.Staging,
		"kind":            models.PanelCertKindMail,
		"cert_pem_path":   certPath,
		"force_deploy":    true,
	})
	if err != nil {
		r.failMailHostnameSwitchover(ctx, desired, services.HumanizePanelCertError(desired, err.Error()), mailHostSwitchoverIssueRetry)
		return "", false
	}
	var resp struct {
		IssuedAt  string `json:"issued_at"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.failMailHostnameSwitchover(ctx, desired, "agent response unmarshal: "+err.Error(), mailHostSwitchoverIssueRetry)
		return "", false
	}
	issuedAt, err1 := time.Parse(time.RFC3339, resp.IssuedAt)
	expiresAt, err2 := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err1 != nil || err2 != nil {
		r.failMailHostnameSwitchover(ctx, desired, "agent response timestamp parse failed", mailHostSwitchoverIssueRetry)
		return "", false
	}

	// JAB-408: issued is not served. A stale deploy hook can leave Stalwart
	// on the old certificate while ssl.panel.issue reports success, so check
	// what IMAPS and SMTPS serve before the name is applied. A retry re-runs
	// the deploy hook, which restarts Stalwart, so it waits the ACME retry.
	if msg := r.checkMailCertServed(ctx, desired); msg != "" {
		r.failMailHostnameSwitchover(ctx, desired, msg, mailHostSwitchoverIssueRetry)
		return "", false
	}

	// Re-check right before applying: issuing took minutes, and a domain
	// created meanwhile must not end up answering the applied name. The
	// certificate still covers mail.<hostname>, so the box keeps serving.
	if err := r.checkMailHostname(ctx, s, desired); err != nil {
		r.failMailHostnameSwitchover(ctx, desired, err.Error(), mailHostSwitchoverNotReadyRetry)
		return "", false
	}

	var applied *string
	if desired != derived {
		applied = &desired
	}
	if err := r.mailHostSwitchover.Complete(ctx, desired, applied, issuedAt, expiresAt, time.Now()); err != nil {
		// The certificate is deployed but the name is not applied. A
		// changed request is picked up next tick; any other error leaves
		// the attempt issuing, and it is re-run once stale.
		r.log.Warn("mail hostname switchover: complete", "desired", desired, "error", err)
		return "", false
	}
	r.log.Info("mail hostname switchover complete", "mail_hostname", desired, "expires_at", expiresAt)
	return desired, true
}

// checkMailCertServed asks the Agent whether the mail server serves the
// certificate just issued for desired. It returns "" when it does, and
// otherwise the reason to record on the switchover.
func (r *Reconciler) checkMailCertServed(ctx context.Context, desired string) string {
	callCtx, cancel := context.WithTimeout(ctx, mailHostSwitchoverServedTimeout)
	defer cancel()
	raw, err := r.agent.Call(callCtx, "ssl.panel.mail_served", map[string]any{"hostname": desired})
	if err != nil {
		return fmt.Sprintf("the certificate for %s was issued, but checking that the mail server serves it failed: %v", desired, err)
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Sprintf("the certificate for %s was issued, but the served-certificate check returned an unreadable answer: %v", desired, err)
	}
	if resp.OK {
		return ""
	}
	if resp.Reason == "" {
		return fmt.Sprintf("the certificate for %s was issued, but the mail server does not serve it", desired)
	}
	return resp.Reason
}

// mailHostSwitchoverDue reports whether sw has an attempt to run now:
// pending, failed with its retry time reached, or issuing but stale. Claim
// re-checks this atomically; this cheap check keeps a request that is not
// due from costing DNS lookups every tick.
func mailHostSwitchoverDue(sw *models.MailHostnameSwitchover, now time.Time) bool {
	switch sw.Status {
	case models.MailHostnameSwitchoverPending:
		return true
	case models.MailHostnameSwitchoverFailed:
		return sw.NextRetryAt == nil || !sw.NextRetryAt.After(now)
	case models.MailHostnameSwitchoverIssuing:
		return now.Sub(sw.UpdatedAt) >= mailHostSwitchoverStaleAfter
	}
	return false
}

// checkMailHostname runs the setter's name check (mailhostops.CheckName)
// against the current domains and web aliases.
func (r *Reconciler) checkMailHostname(ctx context.Context, s *models.ServerSettings, desired string) error {
	deps := mailhostops.NameDeps{Domains: r.domains}
	if r.webDomainAliases != nil {
		deps.Aliases = r.webDomainAliases
	}
	return mailhostops.CheckName(ctx, deps, s, desired)
}

func (r *Reconciler) failMailHostnameSwitchover(ctx context.Context, desired, msg string, retryIn time.Duration) {
	now := time.Now()
	r.log.Warn("mail hostname switchover failed", "desired", desired, "reason", msg, "retry_in", retryIn)
	if err := r.mailHostSwitchover.Fail(ctx, desired, msg, now.Add(retryIn), now); err != nil {
		r.log.Warn("mail hostname switchover: record failure", "desired", desired, "error", err)
	}
}

// assertWebmailRedirect keeps the /webmail redirects in the default vhost on
// host. install.sh renders them only when it runs, so without this they would
// point at the old name until the next `jabali update`. The Agent verb is a
// no-op when the file or the redirects are absent, and reloads nginx only on
// a change.
func (r *Reconciler) assertWebmailRedirect(ctx context.Context, host string) {
	if host == "" {
		return
	}
	params := map[string]any{"mail_hostname": host}
	_, err := r.project(ctx, PhaseWebmailRedirect, "jabali-default", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		_, err := r.agent.Call(callCtx, "nginx.webmail_redirect.apply", params)
		return err
	})
	if err == nil {
		r.webmailRedirectLastErr = ""
		return
	}
	if key := host + "|" + err.Error(); key != r.webmailRedirectLastErr {
		r.webmailRedirectLastErr = key
		r.log.Warn("webmail redirect apply failed", "mail_hostname", host, "error", err)
	}
}

// assertWebmailJMAPURL keeps Bulwark's JMAP_SERVER_URL on host. The Agent
// verb is a no-op on a box without Bulwark and restarts it only on a change.
func (r *Reconciler) assertWebmailJMAPURL(ctx context.Context, host string) {
	if host == "" {
		return
	}
	params := map[string]any{"mail_hostname": host}
	_, err := r.project(ctx, PhaseWebmailJMAPURL, "jabali-webmail", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		_, err := r.agent.Call(callCtx, "webmail.jmap_url.apply", params)
		return err
	})
	if err == nil {
		r.webmailJMAPLastErr = ""
		return
	}
	// An Agent without the verb (mid-rollout) fails every tick; warn once
	// per distinct error.
	if key := host + "|" + err.Error(); key != r.webmailJMAPLastErr {
		r.webmailJMAPLastErr = key
		r.log.Warn("webmail JMAP URL apply failed", "mail_hostname", host, "error", err)
	}
}
