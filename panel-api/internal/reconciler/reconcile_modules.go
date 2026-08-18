package reconciler

import (
	"context"
	"encoding/json"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// M353 module install-on-enable — convergence pass.
//
// The transition-triggered install (panel-api settings PATCH) only fires on a
// false→true flip. But on a --minimal install the module flags can default On
// while nothing was installed (seed_module_flags failure + model default 1) —
// there is no flip to trigger, so the reported "DNS On / pdns inactive" host is
// only fixable by a convergence pass. Every reconcile tick we probe each
// enabled module's real state and, if it isn't installed+active, dispatch a
// (detached, backoff-gated) install.
//
// All optional modules install.sh supports at runtime are converged (dns, mail,
// quota, security).

// moduleInstallRetryInterval bounds how often a single module's install is
// re-dispatched. Long enough that a persistently-failing install doesn't hot-
// loop apt; short enough that an operator retry converges within a coffee break.
const moduleInstallRetryInterval = 15 * time.Minute

// convergedModules lists the module keys the reconciler will install on demand,
// in dependency order (a module's dependsOn must appear earlier). Keep in sync
// with install.sh's --install-module dispatcher + the panel-api PATCH dispatch
// (serverSettingsHandler.dispatchModuleInstall).
var convergedModules = []struct {
	key       string
	dependsOn string // module that must be installed+active first ("" = none)
	enabled   func(f moduleFlags) bool
}{
	{key: "dns", enabled: func(f moduleFlags) bool { return f.dns }},
	{key: "mail", dependsOn: "dns", enabled: func(f moduleFlags) bool { return f.mail }},
	{key: "quota", enabled: func(f moduleFlags) bool { return f.quota }},
	{key: "security", enabled: func(f moduleFlags) bool { return f.security }},
	// GH #1053: ftp (vsftpd) is opt-in via server_settings.ftp_enabled
	// (default OFF) — this converger is also the install-on-enable path
	// for the admin toggle.
	{key: "ftp", enabled: func(f moduleFlags) bool { return f.ftp }},
}

type moduleFlags struct {
	dns      bool
	mail     bool
	quota    bool
	security bool
	ftp      bool
}

func (r *Reconciler) reconcileModuleInstalls(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil {
		return
	}
	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil {
		return
	}
	flags := moduleFlags{dns: srv.DNSEnabled, mail: srv.MailEnabled, quota: srv.QuotaEnabled, security: srv.SecurityEnabled, ftp: srv.FTPEnabled}
	for _, m := range convergedModules {
		if m.enabled(flags) {
			r.convergeModule(ctx, m.key, m.dependsOn)
			// JAB-260: an enabled+active vsftpd can still be running the WRONG
			// TLS posture — a failed plaintext-tighten (or loosen) leaves the old
			// config, which convergeModule's installed+active check never
			// catches. Compare the LIVE config to the desired ftp_allow_plaintext
			// and re-render on drift.
			if m.key == "ftp" {
				r.convergeFtpConfig(ctx, srv)
			}
			continue
		}
		// A DISABLED module is normally left alone. FTP is the exception
		// (JAB-259): it is a security shutoff, so an incomplete disable —
		// vsftpd still active or unmasked, ports still open — must be converged
		// FAIL-CLOSED here rather than skipped and left exposed until the next
		// full `jabali update`. Every other disabled module stays a no-op.
		if m.key == "ftp" {
			r.convergeFtpDisabled(ctx)
		}
	}
}

// convergeFtpConfig heals FTP TLS-posture drift (JAB-260). An enabled+active
// vsftpd can still be serving a config whose TLS enforcement disagrees with the
// desired ftp_allow_plaintext — a failed tighten left the old (plaintext) config
// running, or a failed loosen left TLS required. It reads the LIVE config via
// ftp.config_status and, on drift (equality mismatch, either direction),
// re-runs system.module.install (which re-renders + restarts). The check runs
// every tick (a cheap read); only the re-render is gated by the "ftp-config"
// backoff so a persistent apply failure doesn't hot-loop restarts.
//
// FTP now has THREE reconcile backoff keys: "ftp" (install convergence, in
// convergeModule), "ftp-disable" (convergeFtpDisabled), "ftp-config" (here).
//
// Skew-safe: an old agent without the verb (unknown_command) or a missing conf
// on an active daemon is treated as converged.
func (r *Reconciler) convergeFtpConfig(_ context.Context, srv *models.ServerSettings) {
	if srv == nil || r.agent == nil {
		return
	}
	// Singleflight: at most one drift check in flight. The check runs every tick
	// (fast plaintext-drift detection), so a hung agent socket could otherwise
	// stack goroutines across ticks — this keeps it to one.
	if !r.ftpConfigChecking.CompareAndSwap(false, true) {
		return
	}
	wantSSL := !srv.FTPAllowPlaintext
	go func() {
		defer r.ftpConfigChecking.Store(false)
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		// Only an ACTIVE daemon can serve the wrong posture. An inactive/masked
		// vsftpd serves nothing — the fail-closed end state — and convergeModule
		// owns bringing an installed-but-inactive module back up. Re-rendering an
		// inactive daemon here would fight the no-cert fail-closed mask (a
		// tighten with no cert stops+masks vsftpd) and churn mask/die every tick.
		// So skip unless active; the cert landing + convergeModule heal the rest.
		if installed, active, ok := r.moduleStatus(cctx, "ftp"); !ok || !installed || !active {
			return
		}
		raw, err := r.agent.Call(cctx, "ftp.config_status", map[string]any{})
		if err != nil {
			// Old agent without ftp.config_status → nothing to compare; converged.
			// Debug (not error): fires every tick on every pre-upgrade box.
			r.log.Debug("ftp.config_status unavailable — skipping TLS-drift check", "err", err)
			return
		}
		var st struct {
			Exists      bool `json:"exists"`
			SSLEnforced bool `json:"ssl_enforced"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return
		}
		if !st.Exists {
			// Active daemon but no config file is genuinely odd — warn, but
			// converged-if-active is still the right call (convergeModule owns
			// the install/restart).
			r.log.Warn("ftp active but /etc/vsftpd.conf missing — treating config as converged")
			return
		}
		if st.SSLEnforced == wantSSL {
			return // converged
		}
		// Drift — gate the re-render on its own backoff to avoid hot-looping
		// vsftpd restarts on a persistently failing apply.
		if !r.moduleInstallDue("ftp-config") {
			return
		}
		r.log.Warn("ftp TLS posture drift — re-applying config",
			"want_ssl_enforced", wantSSL, "effective_ssl_enforced", st.SSLEnforced)
		if _, ierr := r.agent.Call(cctx, "system.module.install", map[string]any{"key": "ftp"}); ierr != nil {
			r.log.Error("ftp config re-apply failed (will retry)", "err", ierr)
		}
	}()
}

// convergeFtpDisabled drives FTP toward fail-closed (vsftpd inactive + masked,
// UFW rules removed) when ftp_enabled=false (JAB-259). The dedicated ftp.disable
// verb returns a typed failure while any residual exposure remains; a failure is
// retried on the next eligible tick under the same backoff as install
// convergence (a distinct "ftp-disable" key), so a stubborn systemctl/ufw
// failure keeps converging without hot-looping every tick.
func (r *Reconciler) convergeFtpDisabled(ctx context.Context) {
	if !r.moduleInstallDue("ftp-disable") {
		return
	}
	go func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		// Re-read the flag inside the goroutine: an operator may have re-enabled
		// FTP after this pass was scheduled. Masking a just-re-enabled vsftpd
		// would fight install-convergence and ping-pong under toggle churn, so a
		// stale disable must stand down. (nil serverSettings only in unit tests.)
		if r.serverSettings != nil {
			if srv, err := r.serverSettings.Get(dctx); err == nil && srv != nil && srv.FTPEnabled {
				return
			}
		}
		if _, err := r.agent.Call(dctx, "ftp.disable", map[string]any{}); err != nil {
			r.log.Error("ftp fail-closed disable did not fully converge (will retry)", "err", err)
			return
		}
		r.log.Info("ftp disabled: vsftpd inactive+masked, ports closed")
	}()
}

// convergeModule probes one module's status and, if it isn't fully up, kicks a
// detached install. The probe is quick; the install runs in its own goroutine
// so a multi-minute apt/download run never blocks ReconcileAll.
func (r *Reconciler) convergeModule(ctx context.Context, key, dependsOn string) {
	// A module with an unmet dependency (e.g. mail needs dns's pdns self-zone +
	// jabali_pdns DB) must NOT be dispatched — install.sh would _die. Skip
	// WITHOUT recording a backoff attempt so this module installs promptly on
	// the tick after its dependency converges, not 15 min later.
	if dependsOn != "" {
		di, da, ok := r.moduleStatus(ctx, dependsOn)
		if !ok || !di || !da {
			return
		}
	}

	installed, active, ok := r.moduleStatus(ctx, key)
	if !ok {
		return // agent unreachable — retry next tick, no backoff burned
	}
	// Converged only when BOTH installed AND active. installed-but-inactive
	// (crashed/never-started service — the exact reported bug) must still
	// re-run the install, whose config functions own the start-if-inactive
	// convergence.
	if installed && active {
		return
	}
	if !r.moduleInstallDue(key) {
		return // recently attempted — don't hot-loop on a persistent failure
	}
	go func() {
		ictx, icancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer icancel()
		if _, err := r.agent.Call(ictx, "system.module.install", map[string]any{"key": key}); err != nil {
			r.log.Error("module convergence install failed", "key", key, "err", err)
			return
		}
		r.log.Info("module convergence install dispatched", "key", key)
	}()
}

// moduleStatus probes one module's {installed, active} via the agent. ok=false
// means the agent was unreachable or the response was unparseable.
func (r *Reconciler) moduleStatus(ctx context.Context, key string) (installed, active, ok bool) {
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := r.agent.Call(sctx, "system.module.status", map[string]any{"key": key})
	if err != nil {
		return false, false, false
	}
	var st struct {
		Installed bool `json:"installed"`
		Active    bool `json:"active"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return false, false, false
	}
	return st.Installed, st.Active, true
}

// moduleInstallDue returns true and records the attempt when a module hasn't
// been install-dispatched within moduleInstallRetryInterval. Serializes the
// map access; the agent-side aptMu serializes the installs themselves.
func (r *Reconciler) moduleInstallDue(key string) bool {
	r.moduleInstallMu.Lock()
	defer r.moduleInstallMu.Unlock()
	if r.moduleInstallAttempt == nil {
		r.moduleInstallAttempt = map[string]time.Time{}
	}
	if last, ok := r.moduleInstallAttempt[key]; ok && time.Since(last) < moduleInstallRetryInterval {
		return false
	}
	r.moduleInstallAttempt[key] = time.Now()
	return true
}
