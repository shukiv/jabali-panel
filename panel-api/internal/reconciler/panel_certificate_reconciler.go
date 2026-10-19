package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/panelcert"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// reconcilePanelCertificate is M32's reconciler hook. Post ADR-0105 it
// drives TWO independent rows — the panel hostname cert and the panel
// mail (mail.<hostname>) cert — through the same state machine. The
// mail row's routability preflight is on mail.<hostname>; when it
// fails the mail row parks in pending_acme_retry with a clear reason
// and the hostname row is processed completely independently (mail
// can never block the hostname cert). Both kinds share the single
// admin "Use Let's Encrypt" intent (the hostname row's use_le).
func (r *Reconciler) reconcilePanelCertificate(ctx context.Context) {
	if r.panelCerts == nil || r.panelCertRoutability == nil || r.serverSettings == nil {
		return
	}

	settings, err := r.settingsGet(ctx)
	if err != nil {
		r.log.Debug("panel-cert reconcile skipped: server_settings unavailable", "error", err)
		return
	}

	if _, err := r.panelCerts.EnsureDefault(ctx, settings.Hostname); err != nil {
		r.log.Warn("panel-cert reconcile failed to ensure rows", "error", err)
		return
	}
	hostRow, err := r.panelCerts.GetByKind(ctx, models.PanelCertKindHostname)
	if err != nil {
		r.log.Warn("panel-cert reconcile failed to load hostname row", "error", err)
		return
	}
	// Single admin intent: the hostname row's use_le toggle governs
	// both certs.
	if !hostRow.UseLE {
		// Self-signed mode (no Let's Encrypt): converge the on-disk panel
		// cert to the CURRENT hostname (JAB-389). On a panel hostname change
		// nothing else regenerates the openssl cert install.sh produced, so
		// :8443 keeps serving the OLD hostname's cert until LE is enabled.
		// Disk state is the ONLY source of truth here: the panel_certificate
		// row is set from the cert on disk (GH #2166), never from the RPC's
		// outcome, so the panel restart the agent performs (the Go TLS
		// listener caches the cert and must restart to serve the new one)
		// can kill this reconciler mid-RPC without stranding a row status:
		// the next tick reads the regenerated cert and the row follows it.
		//
		// The mail cert follows the toggle too (GH #2166), when the panel
		// hostname serves mail. It goes first: the hostname's regenerate
		// restarts the panel, which cuts this tick short.
		if r.panelServesMail(ctx, settings) {
			if mailRow, merr := r.panelCerts.GetByKind(ctx, models.PanelCertKindMail); merr == nil {
				r.reconcilePanelMailSelfSignedCert(ctx, mailRow, settings.Hostname)
			} else {
				r.log.Warn("panel-cert reconcile load row", "kind", models.PanelCertKindMail, "error", merr)
			}
		}
		r.reconcilePanelSelfSignedCert(ctx, hostRow, settings.Hostname, settings.PublicIPv4)
		return
	}
	if settings.Hostname == "" || settings.AdminEmail == "" {
		r.log.Debug("panel-cert reconcile skipped: hostname or admin_email empty")
		return
	}

	// The mail kind (mail.<hostname>) is only pursued when the panel hostname
	// actually serves mail — i.e. email is enabled on the panel-primary domain,
	// the SAME gate the webmail vhost uses. Without this a no-mail panel (the
	// default: email_enabled=0) has no mail.<hostname> DNS, so the mail cert
	// parks in pending_acme_retry forever and shows as a perpetual "processing"
	// task (GH #295). Enabling email on the panel hostname turns it back on.
	// A box without the mail module has no mail server for it either.
	panelMailEnabled := r.panelServesMail(ctx, settings)

	for _, kind := range []string{models.PanelCertKindHostname, models.PanelCertKindMail} {
		row, gerr := r.panelCerts.GetByKind(ctx, kind)
		if gerr != nil {
			r.log.Warn("panel-cert reconcile load row", "kind", kind, "error", gerr)
			continue
		}
		if kind == models.PanelCertKindMail && !panelMailEnabled {
			// Don't attempt LE for mail when the panel isn't doing mail. Reset a
			// stale pending row to idle so the UI stops showing it as processing;
			// the self-signed mail cert on disk is harmless (no webmail vhost
			// references it while email is off).
			if row.Status == models.PanelCertStatusPendingACME || row.Status == models.PanelCertStatusPendingACMERetry {
				row.Status = models.PanelCertStatusSelfSigned
				row.LastError = "mail not enabled on the panel hostname — enable email there to request mail.<hostname> TLS"
				if !settings.MailEnabled {
					row.LastError = "the mail module is off — this server runs no mail server for mail.<hostname>"
				}
				row.NextRetryAt = nil
				if uerr := r.panelCerts.Upsert(ctx, row); uerr != nil {
					r.log.Warn("panel-cert reconcile: failed to idle mail row", "error", uerr)
				}
			}
			continue
		}
		r.reconcileOnePanelCert(ctx, kind, row, settings.AdminEmail, settings.PublicIPv4)
	}
}

// panelServesMail reports whether the panel hostname serves mail: the mail
// module is on and email is enabled on the hostname's domain, the same gate
// the webmail vhost uses (GH #295). A domain can keep email_enabled with the
// module off, and on such a box a mail cert push would start a Stalwart that
// isn't installed (GH #2166).
func (r *Reconciler) panelServesMail(ctx context.Context, settings *models.ServerSettings) bool {
	if r.domains == nil || !settings.MailEnabled {
		return false
	}
	d, err := r.domains.FindByName(ctx, settings.Hostname)
	return err == nil && d != nil && d.EmailEnabled
}

// reconcileOnePanelCert runs the ADR-0066 state machine for one kind.
// name is the cert subject for this kind (row.Hostname — the panel
// hostname for kind=hostname, mail.<hostname> for kind=mail). All row
// transitions use the per-kind repo variants so the two rows never
// clobber each other.
func (r *Reconciler) reconcileOnePanelCert(ctx context.Context, kind string, row *models.PanelCertificate, adminEmail, publicIPv4 string) {
	name := row.Hostname
	if name == "" {
		return
	}

	switch row.Status {
	case models.PanelCertStatusSelfSigned:
		// First attempt — fall through to dispatch.
	case models.PanelCertStatusPendingACMERetry:
		if row.NextRetryAt == nil || row.NextRetryAt.After(time.Now()) {
			return
		}
	case models.PanelCertStatusPendingACME:
		if time.Since(row.UpdatedAt) < 10*time.Minute {
			return
		}
		r.log.Warn("panel-cert: stale pending_acme lock, retrying", "kind", kind, "name", name, "stuck_for", time.Since(row.UpdatedAt))
	case models.PanelCertStatusIssued:
		return
	case models.PanelCertStatusFailed:
		return
	}

	gate, err := r.panelCertRoutability.Check(ctx, name, publicIPv4, kind == models.PanelCertKindMail)
	if err != nil {
		r.log.Warn("panel-cert routability check errored", "kind", kind, "name", name, "error", err)
		return
	}
	if !gate.Routable {
		// Mail kind not pointed at this server (e.g. mail.<hostname>
		// is Cloudflare-fronted) — park the MAIL row with a clear
		// reason on its own retry schedule. The hostname row is
		// untouched: mail never blocks the panel hostname cert.
		reason := "not routable: " + gate.Reason
		if kind == models.PanelCertKindMail {
			reason = "mail DNS not pointed at this server (" + name + " -> " + gate.Reason + ")"
		}
		r.log.Debug("panel-cert reconcile parked: not routable", "kind", kind, "name", name, "reason", gate.Reason)
		_ = r.panelCerts.MarkPendingRetryKind(ctx, kind, reason, 3*time.Hour)
		return
	}

	// Mark in-flight (per kind) before the agent call so a concurrent
	// tick / REST issue doesn't double-dispatch this kind.
	row.Status = models.PanelCertStatusPendingACME
	if err := r.panelCerts.Upsert(ctx, row); err != nil {
		r.log.Warn("panel-cert reconcile pre-dispatch upsert failed", "kind", kind, "error", err)
		return
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	// kind + cert_pem_path are forward-compat for the per-kind deploy
	// target (Wave 3 agent wiring honours them; older agents ignore
	// unknown fields). extra_hostnames stays empty — each kind is a
	// single-name cert, independence is the whole point of ADR-0105.
	raw, agentErr := r.agent.Call(dispatchCtx, "ssl.panel.issue", map[string]any{
		"hostname":        name,
		"extra_hostnames": []string{},
		"email":           adminEmail,
		"staging":         row.Staging,
		"kind":            kind,
		"cert_pem_path":   row.CertPEMPath,
	})
	if agentErr != nil {
		r.log.Warn("panel-cert ssl.panel.issue failed", "kind", kind, "name", name, "error", agentErr)
		failReason := agentErr.Error()
		if kind == models.PanelCertKindMail {
			failReason = services.HumanizePanelCertError(name, failReason)
		}
		_ = r.panelCerts.MarkPendingRetryKind(ctx, kind, failReason, 3*time.Hour)
		return
	}

	var resp struct {
		IssuedAt  string `json:"issued_at"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		_ = r.panelCerts.MarkPendingRetryKind(ctx, kind, "agent response unmarshal: "+err.Error(), 3*time.Hour)
		return
	}
	issuedAt, err1 := time.Parse(time.RFC3339, resp.IssuedAt)
	expiresAt, err2 := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err1 != nil || err2 != nil {
		_ = r.panelCerts.MarkPendingRetryKind(ctx, kind, "agent response timestamp parse failed", 3*time.Hour)
		return
	}
	if err := r.panelCerts.MarkIssuedKind(ctx, kind, issuedAt, expiresAt); err != nil {
		r.log.Warn("panel-cert MarkIssued failed", "kind", kind, "error", err)
		return
	}
	r.log.Info("panel-cert issued", "kind", kind, "name", name, "expires_at", expiresAt)
}

// panelSelfSignOrgMarker is the issuer Organization of the panel's own
// self-signed cert (panelcert.SelfSignOrg).
const panelSelfSignOrgMarker = panelcert.SelfSignOrg

// reconcilePanelSelfSignedCert converges /etc/jabali/tls/panel.crt to the
// panel's current hostname while the panel is in self-signed mode (JAB-389).
// It reads and parses the cert locally — no agent round-trip to DECIDE — and
// dispatches ssl.panel.selfsign only when the cert has drifted from hostname.
// The on-disk cert is the only convergence signal: the panel_certificate row
// is set from it, never from the dispatch's outcome (see the caller).
func (r *Reconciler) reconcilePanelSelfSignedCert(ctx context.Context, row *models.PanelCertificate, hostname, publicIPv4 string) {
	if hostname == "" {
		return
	}
	certPath := row.CertPEMPath
	if certPath == "" {
		certPath = panelcert.DefaultCertPath
	}
	v := r.panelSelfSignedVerdict(certPath, hostname)
	// GH #2166: the row follows the cert too, so the Panel SSL tab doesn't
	// show a Let's Encrypt retry or error that doesn't apply with Let's
	// Encrypt off. SetSelfSignedState leaves a row with use_le on alone.
	if r.panelCerts != nil {
		if status, exp, changed := models.SelfSignedState(row, v); changed {
			if err := r.panelCerts.SetSelfSignedState(ctx, models.PanelCertKindHostname, status, exp); err != nil {
				r.log.Warn("panel self-signed cert: row update failed", "error", err)
			}
		}
	}
	if v.Action != panelcert.Regenerate || r.agent == nil {
		return
	}
	reason := v.Reason
	if _, err := r.agent.Call(ctx, "ssl.panel.selfsign", map[string]any{
		"hostname": hostname,
		"ip":       publicIPv4,
	}); err != nil {
		// Transient, or an old agent without the verb during a fleet
		// rollout. Warn once per (hostname,error) so a persistently
		// failing dispatch doesn't spam every tick; the next tick retries
		// naturally.
		key := hostname + "|" + err.Error()
		if r.panelSelfSignLastErr == key {
			r.log.Debug("panel self-signed cert regen still failing", "hostname", hostname, "error", err)
			return
		}
		r.panelSelfSignLastErr = key
		r.log.Warn("panel self-signed cert regen dispatch failed", "hostname", hostname, "error", err)
		return
	}
	r.panelSelfSignLastErr = ""
	r.log.Info("panel self-signed cert regen dispatched", "hostname", hostname, "reason", reason)
}

// reconcilePanelMailSelfSignedCert does for the panel mail cert what
// reconcilePanelSelfSignedCert does for the hostname cert (GH #2166): with
// Let's Encrypt off the mail cert follows the toggle too. It is checked
// against panelcert.MailNames, the row follows it, and ssl.panel.mail_selfsign
// is dispatched only when it must be regenerated, so Stalwart is never
// restarted on a tick with nothing to do. A live CA cert is kept until it
// expires.
func (r *Reconciler) reconcilePanelMailSelfSignedCert(ctx context.Context, row *models.PanelCertificate, hostname string) {
	names := panelcert.MailNames(row.Hostname, hostname)
	if len(names) == 0 {
		return
	}
	certPath := row.CertPEMPath
	if certPath == "" {
		certPath = panelcert.DefaultMailCertPath
	}
	data, err := r.readCertFile(certPath)
	if err != nil {
		data = nil
	}
	v := panelcert.CheckNames(data, names[0], names, time.Now())
	if r.panelCerts != nil {
		if status, exp, changed := models.SelfSignedState(row, v); changed {
			if err := r.panelCerts.SetSelfSignedState(ctx, models.PanelCertKindMail, status, exp); err != nil {
				r.log.Warn("panel mail self-signed cert: row update failed", "error", err)
			}
		}
	}
	if v.Action != panelcert.Regenerate || r.agent == nil {
		return
	}
	if _, err := r.agent.Call(ctx, "ssl.panel.mail_selfsign", map[string]any{"names": names}); err != nil {
		// Debounced like the hostname dispatch: an old agent without the
		// verb during a rollout fails every tick.
		key := strings.Join(names, ",") + "|" + err.Error()
		if r.panelMailSelfSignLastErr == key {
			r.log.Debug("panel mail self-signed cert regen still failing", "names", names, "error", err)
			return
		}
		r.panelMailSelfSignLastErr = key
		r.log.Warn("panel mail self-signed cert regen dispatch failed", "names", names, "error", err)
		return
	}
	r.panelMailSelfSignLastErr = ""
	r.log.Info("panel mail self-signed cert regenerated", "names", names, "reason", v.Reason)
}

// panelSelfSignedCertDrifted reports whether the self-signed panel cert at
// certPath needs regenerating for hostname, plus a short reason for logging.
// panelcert.Check decides, the same way the admin's "Regenerate self-signed"
// action and the agent do: a missing, unparseable, mismatched or expired cert
// drifts. A real CA's cert is PRESERVED until it expires, so a stale
// self-signed dispatch cannot clobber a live Let's Encrypt cert.
func (r *Reconciler) panelSelfSignedCertDrifted(certPath, hostname string) (bool, string) {
	v := r.panelSelfSignedVerdict(certPath, hostname)
	return v.Action == panelcert.Regenerate, v.Reason
}

// panelSelfSignedVerdict reads the cert at certPath and classifies it for
// hostname with panelcert.Check.
func (r *Reconciler) panelSelfSignedVerdict(certPath, hostname string) panelcert.Verdict {
	data, err := r.readCertFile(certPath)
	if err != nil {
		data = nil
	}
	return panelcert.Check(data, hostname, time.Now())
}
