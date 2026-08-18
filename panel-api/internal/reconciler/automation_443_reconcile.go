package reconciler

import (
	"context"
	"time"
)

// reconcileAutomation443 converges the opt-in Automation-API-on-443 nginx
// include (GH #1161) to server_settings.automation_api_public_enabled. When the
// admin flips the toggle it calls the agent's nginx.automation_public_set, which
// writes /etc/nginx/snippets/jabali-automation-443.conf (the location proxy when
// on, empty when off) and reloads nginx. Applied-state-gated: once synced, every
// steady-state tick is a pure noop. On panel-api restart the cache resets to nil
// so the first tick re-asserts the desired state (idempotent — the agent skips
// the reload when the file already matches). On a failed sync it does NOT cache
// the applied value, so the next tick retries.
func (r *Reconciler) reconcileAutomation443(ctx context.Context) {
	if r.serverSettings == nil || r.agent == nil {
		return
	}

	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	s, err := r.serverSettings.Get(c)
	cancel()
	if err != nil || s == nil {
		return
	}
	desired := s.AutomationApiPublicEnabled

	if r.automation443Applied != nil && *r.automation443Applied == desired {
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := r.agent.Call(cctx, "nginx.automation_public_set", map[string]any{
		"enabled": desired,
	}); err != nil {
		r.log.Warn("automation-on-443 sync failed", "error", err)
		return
	}
	applied := desired
	r.automation443Applied = &applied
	r.log.Debug("automation-on-443 converged", "enabled", desired)
}
