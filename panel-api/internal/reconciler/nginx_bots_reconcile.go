package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/useragents"
)

// blockedUserAgents is the admin's server-wide blocked list from Server
// Settings. The API stores only checked entries; an entry that no longer
// passes the rules (edited in the DB by hand) is left out rather than sent
// to nginx.
func (r *Reconciler) blockedUserAgents(ctx context.Context) []string {
	if r.serverSettings == nil {
		return nil
	}
	s, err := r.settingsGet(ctx)
	if err != nil || s == nil || strings.TrimSpace(s.BlockedUserAgents) == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s.BlockedUserAgents, "\n") {
		if e := strings.TrimSpace(line); e != "" && useragents.Valid(e) {
			out = append(out, e)
		}
	}
	list, err := useragents.Normalize(out)
	if err != nil {
		r.log.Warn("reconcile nginx-bots: blocked user agents", "err", err)
		if len(out) > useragents.MaxBlocked {
			out = out[:useragents.MaxBlocked]
		}
		list, _ = useragents.Normalize(out)
	}
	return list
}

// ReconcileNginxBots renders /etc/nginx/conf.d/00-jabali-bots.conf (the
// AI-crawler map and the admin's blocked list) through the agent's
// nginx.bots.apply, which tests nginx and reloads it when the file changed.
// A vhost refers to the maps only when it blocks something, and domain.create
// writes a default fragment when none exists, so this need not run before
// the domain loop for a vhost to pass nginx -t; it runs first so a newly
// blocked agent is refused in the same run.
func (r *Reconciler) ReconcileNginxBots(ctx context.Context) {
	if r.agent == nil {
		return
	}
	list := r.blockedUserAgents(ctx)
	if list == nil {
		list = []string{}
	}
	params := map[string]any{"blocked_user_agents": list}
	_, _ = r.project(ctx, PhaseNginxBots, "fragment", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		raw, err := r.agent.Call(callCtx, "nginx.bots.apply", params)
		if err != nil {
			r.log.Warn("reconcile nginx-bots: apply failed", "err", err, "count", len(list))
			return err
		}
		var resp struct {
			NoChange bool `json:"no_change"`
		}
		if json.Unmarshal(raw, &resp) == nil && !resp.NoChange {
			r.log.Info("reconcile nginx-bots: fragment updated", "blocked", len(list))
		}
		return nil
	})
}
