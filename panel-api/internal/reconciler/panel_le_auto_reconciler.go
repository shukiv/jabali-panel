package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// panelLEAutoCheckEvery spaces out the public DNS check while a new install
// waits for its panel hostname: the check asks outside resolvers, and where
// outbound DNS is blocked each one takes seconds to give up.
const panelLEAutoCheckEvery = 10 * time.Minute

// panelLEAutoCheckTimeout bounds one public DNS check.
const panelLEAutoCheckTimeout = 10 * time.Second

// panelLEAutoGrace is how long after the window opens the first check waits.
// The row is created on the panel's first pass, possibly while install.sh is
// still setting up nginx, certbot and the agent, and a first attempt that
// fails for that turns Let's Encrypt back off for good.
const panelLEAutoGrace = 15 * time.Minute

// reconcilePanelLEAuto runs while a new install's Let's Encrypt window is
// open (models.PanelCertificate.LEAutoUntil, ADR-0066 amendment). It reports
// whether it changed use_le, which ends the pass: the next one works from it.
//
// With Let's Encrypt still off it waits for the panel hostname, from
// panelLEAutoGrace after the window opens. Once public DNS shows it at this
// server, and so does the routability check the issue runs, it turns Let's
// Encrypt on, and the next pass issues. That includes a free
// <label>.jabalihosted.com name: its certificates share one Let's Encrypt
// allowance with every such server (50 new a week, renewals exempt), but a
// new server's one certificate is what that allowance is for. It doesn't
// without an admin email to register with. When the window passes, Let's
// Encrypt is left to the admin.
//
// With Let's Encrypt on, it follows that first, automatic issue. Issued ends
// the window. A failed attempt turns Let's Encrypt back off, so a server
// whose admin asked for nothing stays self-signed instead of retrying every
// three hours.
func (r *Reconciler) reconcilePanelLEAuto(ctx context.Context, row *models.PanelCertificate, settings *models.ServerSettings) bool {
	now := time.Now()
	if row.UseLE {
		switch row.Status {
		case models.PanelCertStatusIssued:
			if err := r.panelCerts.EndLEAuto(ctx); err != nil {
				r.log.Warn("panel Let's Encrypt (new install): failed to end the window", "error", err)
				return false
			}
			r.log.Info("panel Let's Encrypt (new install): certificate issued", "hostname", row.Hostname)
		case models.PanelCertStatusPendingACMERetry, models.PanelCertStatusFailed:
			changed, err := r.panelCerts.RevertLEAuto(ctx)
			if err != nil {
				r.log.Warn("panel Let's Encrypt (new install): failed to turn it back off", "error", err)
				return false
			}
			if changed {
				r.log.Warn("panel Let's Encrypt (new install): the certificate could not be issued, so Let's Encrypt is off again and the panel stays self-signed; turn it on in SSL Manager → Panel SSL to try again",
					"hostname", row.Hostname, "error", row.LastError)
				return true
			}
		}
		return false
	}

	if !row.LEAutoUntil.After(now) {
		if err := r.panelCerts.EndLEAuto(ctx); err != nil {
			r.log.Warn("panel Let's Encrypt (new install): failed to end the window", "error", err)
			return false
		}
		r.log.Info("panel Let's Encrypt (new install): the hostname didn't resolve to this server within the window, so the panel stays self-signed",
			"hostname", settings.Hostname)
		return false
	}
	if now.Sub(row.LEAutoUntil.Add(-models.PanelLEAutoWindow)) < panelLEAutoGrace {
		return false
	}
	host := settings.Hostname
	if host == "" || settings.AdminEmail == "" {
		return false
	}

	r.panelLEAutoMu.Lock()
	due := now.Sub(r.panelLEAutoLastCheck) >= panelLEAutoCheckEvery
	if due {
		r.panelLEAutoLastCheck = now
	}
	r.panelLEAutoMu.Unlock()
	if !due {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, panelLEAutoCheckTimeout)
	defer cancel()
	if !r.panelCertRoutability.ConfirmedPublic(checkCtx, host, settings.PublicIPv4) {
		r.log.Debug("panel Let's Encrypt (new install): the hostname doesn't resolve to this server in public DNS yet",
			"hostname", host, "until", row.LEAutoUntil)
		return false
	}
	// The issue runs the usual routability check, which asks this server's
	// own resolver first. Where that disagrees, the attempt would be parked
	// as not routable, and that turns Let's Encrypt back off: wait instead.
	if gate, err := r.panelCertRoutability.Check(checkCtx, host, settings.PublicIPv4, false); err != nil || !gate.Routable {
		r.log.Debug("panel Let's Encrypt (new install): public DNS shows the hostname here, but this server's resolver doesn't yet",
			"hostname", host, "reason", gate.Reason)
		return false
	}

	changed, err := r.panelCerts.AutoEnableLE(ctx, now)
	if err != nil {
		r.log.Warn("panel Let's Encrypt (new install): failed to turn it on", "error", err)
		return false
	}
	if changed {
		r.log.Info("panel Let's Encrypt (new install): the hostname resolves to this server, so Let's Encrypt is on for the panel",
			"hostname", host)
	}
	return changed
}
