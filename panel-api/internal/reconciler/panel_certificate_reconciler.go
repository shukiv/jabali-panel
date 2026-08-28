package reconciler

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"time"

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

	settings, err := r.serverSettings.Get(ctx)
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
		// Disk state is the ONLY state here — we deliberately never touch the
		// panel_certificate row, so the panel restart the agent performs (the
		// Go TLS listener caches the cert and must restart to serve the new
		// one) can kill this reconciler mid-RPC without stranding a row
		// status: the next tick's drift check on the regenerated cert is the
		// success signal.
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
	panelMailEnabled := false
	if r.domains != nil {
		if d, derr := r.domains.FindByName(ctx, settings.Hostname); derr == nil && d != nil {
			panelMailEnabled = d.EmailEnabled
		}
	}

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


// panelSelfSignOrgMarker is the issuer Organization our self-signed panel
// cert carries (ssl.panel.selfsign / install.sh provision_tls_cert stamp
// "/O=Jabali Panel"). A cert whose issuer Organization is neither empty nor
// this marker is a real CA cert (Let's Encrypt / custom) and must never be
// clobbered — the same guard install.sh applies after the 2026-05-09
// LE-clobber incident.
const panelSelfSignOrgMarker = "Jabali Panel"

// reconcilePanelSelfSignedCert converges /etc/jabali/tls/panel.crt to the
// panel's current hostname while the panel is in self-signed mode (JAB-389).
// It reads and parses the cert locally — no agent round-trip to DECIDE — and
// dispatches ssl.panel.selfsign only when the cert has drifted from hostname.
// It is stateless with respect to the panel_certificate row (see the caller):
// the on-disk cert is the only convergence signal.
func (r *Reconciler) reconcilePanelSelfSignedCert(ctx context.Context, row *models.PanelCertificate, hostname, publicIPv4 string) {
	if r.agent == nil || hostname == "" {
		return
	}
	certPath := row.CertPEMPath
	if certPath == "" {
		certPath = "/etc/jabali/tls/panel.crt"
	}
	drift, reason := r.panelSelfSignedCertDrifted(certPath, hostname)
	if !drift {
		return
	}
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

// panelSelfSignedCertDrifted reports whether the self-signed panel cert at
// certPath needs regenerating for hostname, plus a short reason for logging.
// A missing/unparseable cert drifts (regenerate). A cert issued by a real CA
// (issuer Organization neither empty nor panelSelfSignOrgMarker) is PRESERVED
// — never reported as drift — so a stale self-signed dispatch cannot clobber
// a deployed Let's Encrypt cert.
func (r *Reconciler) panelSelfSignedCertDrifted(certPath, hostname string) (bool, string) {
	data, err := r.readCertFile(certPath)
	if err != nil {
		return true, "cert missing or unreadable"
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return true, "cert not PEM"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true, "cert unparseable"
	}
	if o := panelCertIssuerOrg(cert); o != "" && o != panelSelfSignOrgMarker {
		return false, ""
	}
	if !strings.EqualFold(cert.Subject.CommonName, hostname) {
		return true, "CN " + cert.Subject.CommonName + " != " + hostname
	}
	for _, need := range []string{hostname, "mail." + hostname} {
		found := false
		for _, d := range cert.DNSNames {
			if strings.EqualFold(d, need) {
				found = true
				break
			}
		}
		if !found {
			return true, "missing SAN " + need
		}
	}
	// Expiry symmetry with the agent's no-churn check: an expired self-signed
	// cert still covering the hostname must self-heal, else the reconciler
	// would say "no drift" and never ask the agent to regenerate. The fresh
	// cert passes this check, so there is no per-tick regen loop.
	if !cert.NotAfter.After(time.Now()) {
		return true, "cert expired"
	}
	return false, ""
}

// panelCertIssuerOrg returns the first issuer Organization of cert, trimmed,
// or "" when absent.
func panelCertIssuerOrg(cert *x509.Certificate) string {
	if len(cert.Issuer.Organization) > 0 {
		return strings.TrimSpace(cert.Issuer.Organization[0])
	}
	return ""
}
