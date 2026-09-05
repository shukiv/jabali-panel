package reconciler

import (
	"context"
	"regexp"
	"strings"
)

// kratosConfigPath is the rendered Kratos config on the box. panel-api can read
// it: its AppArmor profile allows `/etc/jabali-panel/** r`, and the file is
// root:<service-user> mode 0640 while panel-api runs as the service user.
const kratosConfigPath = "/etc/jabali-panel/kratos.yml"

// kratosBaseURLHostRE captures the panel host from the serve.public base_url
// line, mirroring the agent verb's extractor.
var kratosBaseURLHostRE = regexp.MustCompile(`(?m)^\s*base_url:\s*"https://([a-zA-Z0-9.-]+)(?::[0-9]+)?/`)

// reconcileKratosHostname converges kratos.yml's panel hostname to
// server_settings.hostname (JAB-393). Changing the panel FQDN never regenerates
// kratos.yml — it is rendered only by install.sh — so Kratos keeps emitting
// URLs for, and allowing CORS only from, the OLD origin, which CORS-blocks
// every login (a full lockout). This reads the base_url host on disk (disk is
// the convergence signal, the same way the panel-cert reconciler reads the
// cert) and dispatches kratos.config.rehost on drift. Being stateless, it also
// self-heals a box already broken by a past rename and re-heals after a
// `jabali update` re-renders kratos.yml from a stale config.toml hostname.
func (r *Reconciler) reconcileKratosHostname(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil || r.readKratosConfigFile == nil {
		return
	}
	settings, err := r.serverSettings.Get(ctx)
	if err != nil || settings == nil || settings.Hostname == "" {
		return
	}
	data, err := r.readKratosConfigFile(kratosConfigPath)
	if err != nil {
		// A rewriter has nothing to fix if the config is missing/unreadable.
		r.warnKratosRehostOnce("read kratos.yml: " + err.Error())
		return
	}
	m := kratosBaseURLHostRE.FindSubmatch(data)
	if m == nil {
		r.warnKratosRehostOnce("kratos.yml has no serve.public.base_url host")
		return
	}
	current := string(m[1])
	if strings.EqualFold(current, settings.Hostname) {
		r.kratosRehostLastErr = ""
		return
	}
	if _, err := r.agent.Call(ctx, "kratos.config.rehost", map[string]any{
		"hostname": settings.Hostname,
	}); err != nil {
		r.warnKratosRehostOnce("kratos.config.rehost dispatch failed: " + err.Error())
		return
	}
	r.kratosRehostLastErr = ""
	r.log.Info("kratos hostname rehost dispatched", "from", current, "to", settings.Hostname)
}

// warnKratosRehostOnce logs a rehost problem at Warn only when it changes,
// then at Debug while it persists — so a mid-rollout agent without the verb, or
// a persistently unreadable config, doesn't spam Warn every tick.
func (r *Reconciler) warnKratosRehostOnce(detail string) {
	if r.kratosRehostLastErr == detail {
		r.log.Debug("kratos hostname rehost still blocked", "detail", detail)
		return
	}
	r.kratosRehostLastErr = detail
	r.log.Warn("kratos hostname rehost", "detail", detail)
}
