package reconciler

import (
	"context"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

// reconcileStalwartWebadmin re-applies the Stalwart WebAdmin door (its vhost
// and the OAuthClient its sign-in needs, GH #2542) once per panel process, and
// again whenever the hostname or the allowlist changes. Saving the setting
// applies it at once; this pass covers servers that turned the door on before
// the agent registered the client, and a panel hostname change, which moves
// the WebAdmin's address and so its OAuth client id. Turning the door off is
// left to the settings save, which removes the vhost and the client.
func (r *Reconciler) reconcileStalwartWebadmin(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil {
		return
	}
	s, err := r.settingsGet(ctx)
	if err != nil || s == nil || !s.StalwartWebadminEnabled || s.Hostname == "" {
		return
	}
	key := strings.ToLower(s.Hostname) + "|" + s.StalwartWebadminAllowCIDRs
	if key == r.webadminApplied {
		return
	}
	if _, err := r.agent.Call(ctx, "mail.webadmin.apply", settingsops.WebadminApplyParams(s)); err != nil {
		r.warnWebadminOnce("mail.webadmin.apply: " + err.Error())
		return
	}
	r.webadminApplied, r.webadminLastErr = key, ""
	r.log.Info("stalwart webadmin applied", "hostname", s.Hostname)
}

// warnWebadminOnce logs at Warn only when the problem changes (mail off, an
// agent without the client registration, Stalwart down), then at Debug.
func (r *Reconciler) warnWebadminOnce(detail string) {
	if r.webadminLastErr == detail {
		r.log.Debug("stalwart webadmin apply still failing", "detail", detail)
		return
	}
	r.webadminLastErr = detail
	r.log.Warn("stalwart webadmin apply", "detail", detail)
}
