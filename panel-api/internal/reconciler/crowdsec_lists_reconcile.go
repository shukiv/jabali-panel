package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/blocklists"
)

// crowdsecBlocklists is the admin's extra blocklists from Server Settings,
// as catalog keys. A key no longer in the catalog is left out.
func crowdsecBlocklists(stored string) []string {
	var keys []string
	for _, k := range strings.Split(stored, ",") {
		if _, ok := blocklists.Lookup(strings.TrimSpace(k)); ok {
			keys = append(keys, strings.TrimSpace(k))
		}
	}
	out, _ := blocklists.Normalize(keys)
	return out
}

// reconcileCrowdsecLists sends the admin's extra blocklists to the agent's
// security.crowdsec.lists.apply, which imports the lists that are on and
// removes the bans of the ones that are off, in the background. The set and
// this host's public IPs are the fingerprint, so a change is sent on the
// next run; the phase's daily interval re-sends it, which refreshes every
// list. With no list on, the agent only removes bans a list left behind.
func (r *Reconciler) reconcileCrowdsecLists(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil {
		return
	}
	s, err := r.settingsGet(ctx)
	if err != nil || s == nil || !s.SecurityEnabled {
		return
	}
	params := map[string]any{
		"enabled": crowdsecBlocklists(s.CrowdsecBlocklists),
		"custom":  blocklists.ValidCustom(strings.Split(s.CrowdsecCustomBlocklists, "\n")),
		"protect": selfPublicIPs(s.PublicIPv4, s.PublicIPv6),
	}
	_, _ = r.project(ctx, PhaseCrowdsecLists, "all", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := r.agent.Call(callCtx, "security.crowdsec.lists.apply", params); err != nil {
			r.log.Warn("reconcile crowdsec lists: apply failed", "err", err)
			return err
		}
		return nil
	})
}
