package reconciler

import (
	"context"
	"errors"
	"os"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// webmail_reconcile.go: per-domain mail.<domain> nginx vhost convergence
// (M6 Step 8). The reconciler walks every domain on each tick; domains
// with email_enabled=1 get their vhost written via the agent, domains
// that are disabled OR missing get their vhost removed. All calls are
// idempotent on the agent side (content-hash gated) so the no-change
// steady state is just one cheap Read per domain.
//
// Why this lives in the reconciler (not the email_enable HTTP handler):
//
//   - The handler flips email_enabled=1 then calls an agent to register
//     DKIM. If the agent-side vhost write fails, we still want the
//     flag flipped — email is on; Bulwark just isn't serving yet.
//     Reconciler catches up on the next tick.
//   - It makes the after-a-restore / after-agent-crash path trivial:
//     the DB is the source of truth, one tick repairs every vhost.
//   - It doesn't couple the HTTP path to nginx reload latency.
//
// SSL note: v1 passes the main domain's cert paths to the agent. That
// cert probably doesn't list mail.<domain> as a SAN — browsers will
// warn. A proper ACME-for-mail.<domain> flow is M6.1 follow-on.

// webmailAgentTimeout bounds each agent call. Matches nginx's own
// reload budget (reloads can take a few seconds on hosts with many
// vhosts because worker shutdown is serial).
const webmailAgentTimeout = 30 * time.Second

// reconcileWebmailVhosts is invoked from ReconcileAll. Errors are
// logged per-domain and don't abort the sweep; the next tick retries.
// listWebmailDomains lists all domains for the webmail reconcile pass.
func (r *Reconciler) listWebmailDomains(ctx context.Context) ([]models.Domain, error) {
	domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	return domains, err
}

func (r *Reconciler) reconcileWebmailVhosts(ctx context.Context) {
	// GH #316 / #760: webmail disabled server-wide → stop AND disable the
	// Bulwark daemon, clear the AppSec allowlist, tear down vhosts. This runs
	// BEFORE the sslCerts/domains guards below: those guard only vhost
	// RENDERING, but the daemon must be stopped regardless — otherwise, on an
	// install without ACME/SSL wired (sslCerts == nil), disabling webmail left
	// the Node process running (#760). `disable` (not just stop) so it also
	// stays down across reboots.
	if r.serverSettings != nil && r.agent != nil {
		if s, sErr := r.settingsGet(ctx); sErr == nil && s != nil && !s.WebmailEnabled {
			if r.domains != nil {
				if domains, err := r.listWebmailDomains(ctx); err == nil {
					for i := range domains {
						if domains[i].EmailEnabled {
							r.removeWebmailVhost(ctx, domains[i].Name)
						}
					}
				}
			}
			if _, werr := appseccfg.WriteWebmailHosts(appseccfg.WebmailHostsPath, nil); werr != nil {
				r.log.Warn("webmail reconcile: clear webmail-hosts.list", "err", werr)
			}
			r.convergeWebmailDaemon(ctx, false)
			return
		}
	}

	if r.sslCerts == nil {
		// Without SSL cert paths we can't render the vhost (ssl_certificate
		// directive is required). In an M5-less install this hook is a
		// no-op — operators running without ACME won't have webmail
		// either. (The disabled-teardown above already ran.)
		return
	}
	if r.domains == nil {
		return
	}

	domains, err := r.listWebmailDomains(ctx)
	if err != nil {
		r.log.Error("webmail reconcile: list domains", "err", err)
		return
	}

	// GH #316 + #1628: a domain's webmail is OFF if the per-user toggle is OFF
	// (#316) OR the user's hosting package has webmail disabled (#1628). Build
	// one set of user IDs that are OFF for either reason; a user absent from
	// the set = ON (column default 1).
	//
	// No package (PackageID nil) or a dangling package id (package deleted) →
	// NOT in the off-package set → webmail ON. Keeping webmail on for a
	// package-less account is a deliberate #1628 exception to #282's
	// privileged-feature DENY default, decided in the entitlement plan
	// (webmail is a convenience surface, not a hardening clamp).
	//
	// Both List calls FAIL OPEN: on error we log and treat everyone as ON. A
	// transient DB blip must never tear down every tenant's webmail vhost.
	//
	// GH #1628 slice 3: the per-user webmail toggle (users.webmail_enabled, #316)
	// is retired — this gate no longer reads it. Its OFF intent was backfilled
	// down to domains.webmail_enabled (migration 000301), which line 145 already
	// ANDs in. webmailOffUsers is now purely the package-entitlement set.
	webmailOffUsers := map[string]bool{}
	offPackages := map[string]bool{}
	if r.packages != nil {
		if pkgs, _, pErr := r.packages.List(ctx, repository.ListOptions{Limit: 100000}); pErr == nil {
			for i := range pkgs {
				if !pkgs[i].WebmailEnabled {
					offPackages[pkgs[i].ID] = true
				}
			}
		} else {
			r.log.Warn("webmail reconcile: list packages for entitlement gate", "err", pErr)
		}
	}
	if r.users != nil {
		if users, _, uErr := r.users.List(ctx, repository.ListOptions{Limit: 100000}); uErr == nil {
			for i := range users {
				if users[i].PackageID != nil && offPackages[*users[i].PackageID] {
					webmailOffUsers[users[i].ID] = true
				}
			}
		} else {
			r.log.Warn("webmail reconcile: list users for package entitlement gate", "err", uErr)
		}
	}

	anyEmailEnabled := false
	webmailHosts := make([]string, 0, len(domains)*2)
	for i := range domains {
		d := &domains[i]
		if d.EmailEnabled && d.WebmailEnabled && !webmailOffUsers[d.UserID] {
			anyEmailEnabled = true
			r.applyWebmailVhost(ctx, d)
			// Mirror what the agent's mail vhost template emits as
			// server_name: mail.<dom> + autoconfig.<dom> + autodiscover.<dom>.
			// Panel-primary
			// also serves the bare panel hostname but that's never a
			// public WAF-bypass target — it's the panel itself and
			// already covered by the /api/v1/ allowlist.
			webmailHosts = append(webmailHosts, "mail."+d.Name, "autoconfig."+d.Name, "autodiscover."+d.Name)
		} else {
			r.removeWebmailVhost(ctx, d.Name)
		}
	}

	// Write the AppSec webmail-allowlist state file every pass.
	// internal/appseccfg.Render reads this file via LoadWebmailHosts
	// to assemble the on_match block — so CRS rule 911100 doesn't
	// 403 Bulwark's PUT /api/auth/session on the webmail vhosts.
	// Write-on-diff returns changed=true only on real changes; the
	// steady-state cost is a single stat + memcmp. Errors here
	// don't fail the reconcile sweep: the AppSec config still has
	// the previous allowlist on disk, which is the safe state.
	if _, err := appseccfg.WriteWebmailHosts(appseccfg.WebmailHostsPath, webmailHosts); err != nil {
		r.log.Warn("webmail reconcile: write webmail-hosts.list", "err", err)
	}

	// Ensure the Bulwark daemon itself is running whenever any tenant
	// has email enabled. domain.email_enable starts it the first time,
	// but on a clean exit (Restart=on-failure used to leave the unit
	// inactive — fixed alongside this loop by switching to
	// Restart=always) or a manual stop, nothing else converges. The
	// agent's service.start verb is idempotent: a no-op when the unit
	// is already active.
	if anyEmailEnabled {
		r.convergeWebmailDaemon(ctx, true)
	}
}

// convergeWebmailDaemon starts and enables the Bulwark daemon (on) or stops
// and disables it (off). Enable pairs with start so the daemon survives a
// reboot, symmetric with the disable on the off path (#760). JAB-369: gated
// by the webmail.daemon phase; the state is stamped only when both calls
// succeed, so a half-applied transition is retried on the next tick.
func (r *Reconciler) convergeWebmailDaemon(ctx context.Context, on bool) {
	verbs := []string{"service.stop", "service.disable"}
	if on {
		verbs = []string{"service.start", "service.enable"}
	}
	_, _ = r.project(ctx, PhaseWebmailDaemon, "jabali-webmail", fingerprint(verbs), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		var errs []error
		for _, verb := range verbs {
			if _, err := r.agent.Call(callCtx, verb, map[string]any{"name": "jabali-webmail"}); err != nil {
				r.log.Warn("webmail reconcile: "+verb+" jabali-webmail failed", "err", err)
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
}

// ReconcileWebmailVhosts runs the full webmail vhost convergence sweep once.
// Exported so the package-update handler can kick an immediate pass when an
// admin flips a package's webmail entitlement (GH #1628), instead of waiting
// up to a reconcile tick. It is the WHOLE sweep, not a per-user reconcile:
// the pass owns sweep-global side effects — the AppSec webmail-allowlist file
// (webmail-hosts.list, assembled from the full domain set) and the
// jabali-webmail daemon start/stop — that a per-user pass can't reproduce, so
// a per-user kick would leave a newly-gated vhost half-applied (e.g. 403'd by
// CRS 911100 until the next tick). Idempotent + content-hash gated on the
// agent side, so the steady-state cost of an extra pass is cheap.
func (r *Reconciler) ReconcileWebmailVhosts(ctx context.Context) {
	r.reconcileWebmailVhosts(ctx)
}

// panelPrimaryWebmailSSLPaths returns the cert/key for the panel-primary
// domain's mail vhost. That domain has no per-domain ssl_certificates row;
// its cert is the M32 panel-cert system's on-disk files. Prefer the
// dedicated panel-mail cert (Let's Encrypt on a real hostname); fall back
// to the panel hostname cert (self-signed on a .local/internal host, whose
// SAN already covers mail.<hostname> per M6.4). Empty when neither exists.
func panelPrimaryWebmailSSLPaths() (string, string, bool) {
	for _, pair := range [2][2]string{
		{"/etc/jabali/tls/panel-mail.crt", "/etc/jabali/tls/panel-mail.key"},
		{"/etc/jabali/tls/panel.crt", "/etc/jabali/tls/panel.key"},
	} {
		if fileReadable(pair[0]) && fileReadable(pair[1]) {
			return pair[0], pair[1], true
		}
	}
	return "", "", false
}

func fileReadable(p string) bool {
	if _, err := os.Stat(p); err != nil {
		return false
	}
	return true
}

func (r *Reconciler) applyWebmailVhost(ctx context.Context, d *models.Domain) {
	certPath, keyPath, ok := r.webmailSSLPaths(ctx, d.ID)
	if !ok && d.IsPanelPrimary {
		// The panel-primary domain (e.g. mx.jabali-panel.com) has no
		// per-domain ssl_certificates row — its mail cert lives in the
		// M32 panel-cert system on disk. Without this fallback the
		// panel-hostname webmail vhost (mail.<panel-host>) is never
		// built and the host falls to the default landing.
		certPath, keyPath, ok = panelPrimaryWebmailSSLPaths()
	}
	if !ok {
		// No live cert on disk yet — the domain's ACME issuance may be
		// in-flight or M5 might not have run. Skip this tick; the next
		// reconcile pass will retry once the cert materialises.
		r.log.Debug("webmail reconcile: skipping vhost — no usable SSL cert on file",
			"domain_id", d.ID, "domain", d.Name)
		return
	}

	params := map[string]any{
		"domain_name":   d.Name,
		"ssl_cert_path": certPath,
		"ssl_key_path":  keyPath,
		// doc_root is the ACME HTTP-01 webroot for renewals targeting
		// mail.<domain>. Same path ssl.issue uses (-w domain.DocRoot)
		// so renewal challenge files land where nginx will serve them.
		"doc_root": d.DocRoot,
		// M6.6 / 2026-06-01: panel-primary mail vhost includes bare
		// PanelHostname in server_name so Bulwark's /api/auth/impersonate
		// upstream JMAP fetch (https://<panel-hostname>/jmap) routes
		// correctly. Without this, server-side fetch falls to nginx
		// default vhost and returns 500.
		"is_panel_primary": d.IsPanelPrimary,
	}
	// listen_ipv4 / listen_ipv6 — same resolution as the apex vhost
	// (M24). When the apex vhost binds a specific IP, the mail vhost
	// MUST also bind that IP or it falls into nginx's wildcard pool
	// (which gets ignored on IPs with at least one specific listener)
	// and SNI for mail.<domain> lands on the wrong tenant's cert.
	if r.managedIPs != nil {
		if v4 := r.resolveListenIPAddress(ctx, d.ListenIPv4ID, "ipv4"); v4 != "" {
			params["listen_ipv4"] = v4
		}
		if v6 := r.resolveListenIPAddress(ctx, d.ListenIPv6ID, "ipv6"); v6 != "" {
			params["listen_ipv6"] = v6
		}
	}
	addPanelMailHostnameParams(params, r.webmailSettings(ctx), d)
	// JAB-369: gated by the webmail.vhost phase. params is exactly what the
	// Agent receives, so any input to the vhost changes the fingerprint.
	_, _ = r.project(ctx, PhaseWebmailVhost, d.Name, fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		if _, err := r.agent.Call(callCtx, "webmail.vhost_apply", params); err != nil {
			r.log.Error("webmail reconcile: vhost_apply failed",
				"domain_id", d.ID, "domain", d.Name, "err", err)
			return err
		}
		return nil
	})
}

// removeWebmailVhost removes a domain's mail vhost. JAB-369: same
// webmail.vhost ledger entry as the apply, with the removal as its
// fingerprint, so a domain without webmail is not re-sent a removal on
// every tick, and a later apply for it is never skipped.
func (r *Reconciler) removeWebmailVhost(ctx context.Context, domainName string) {
	if domainName == "" {
		return
	}
	params := map[string]any{"domain_name": domainName}
	_, _ = r.project(ctx, PhaseWebmailVhost, domainName, fingerprint(map[string]any{"remove": params}), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		if _, err := r.agent.Call(callCtx, "webmail.vhost_remove", params); err != nil {
			// The remove path is idempotent; a failure here usually means
			// nginx itself is down. Log and move on — next tick retries.
			r.log.Error("webmail reconcile: vhost_remove failed",
				"domain", domainName, "err", err)
			return err
		}
		return nil
	})
}

// webmailSettings returns the server settings row, or nil when settings
// aren't wired (fresh install) or can't be read.
func (r *Reconciler) webmailSettings(ctx context.Context) *models.ServerSettings {
	if r.serverSettings == nil {
		return nil
	}
	s, err := r.settingsGet(ctx)
	if err != nil {
		return nil
	}
	return s
}

// addPanelMailHostnameParams sets the panel-hostname params of a
// webmail.vhost_apply call. They install an nginx sub_filter that keeps the
// Bulwark SPA same-origin against the per-domain mail vhost it was loaded
// from — see the rendered vhost template for the why.
//
//   - panel_hostname: server_settings.hostname (e.g. mx.jabali-panel.com).
//   - panel_mail_hostname (JAB-390): the name Bulwark's JMAP URL carries —
//     the applied custom shared mail hostname, else mail.<hostname> — and so
//     the name the sub_filter rewrites.
//   - extra_server_names, panel-primary row only: an applied custom mail
//     hostname, which that vhost must also answer (its server_name already
//     covers mail.<hostname>).
//
// Nothing is set when s is nil.
func addPanelMailHostnameParams(params map[string]any, s *models.ServerSettings, d *models.Domain) {
	if s == nil {
		return
	}
	if s.Hostname != "" {
		params["panel_hostname"] = s.Hostname
	}
	if h := models.EffectiveMailHostname(s.MailHostname, s.Hostname); h != "" {
		params["panel_mail_hostname"] = h
	}
	if !d.IsPanelPrimary {
		return
	}
	if applied, ok := models.AppliedMailHostname(s.MailHostname); ok && applied != models.PanelMailHostname(d.Name) {
		params["extra_server_names"] = []string{applied}
	}
}

// webmailSSLPaths returns the cert + key paths for a domain if a usable
// certificate is on file. Mirrors the allow-list used by ReconcileOne
// when rendering the main vhost: accept anything not REVOKED with both
// paths populated.
func (r *Reconciler) webmailSSLPaths(ctx context.Context, domainID string) (string, string, bool) {
	sslCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cert, err := r.sslCerts.FindByDomainID(sslCtx, domainID)
	if err != nil || cert == nil {
		return "", "", false
	}
	if cert.Status == models.SSLStatusRevoked {
		return "", "", false
	}
	if cert.CertPath == nil || cert.KeyPath == nil {
		return "", "", false
	}
	return *cert.CertPath, *cert.KeyPath, true
}
