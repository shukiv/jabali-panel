package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const botExemptAgentTimeout = 30 * time.Second

// reconcileBotChallengeExempt keeps the per-domain AppSec bot-detection
// opt-out in sync. Every pass it writes the exempt-hosts state file
// (write-on-diff) from the domains flagged bot_challenge_exempt, expanding
// each to www.<domain>; and, while server-wide bot detection is ON, it asks
// the agent to re-render jabali-bot-exempt.
//
// The agent call is made EVERY pass, not only on a change: refresh_exempt is
// itself write-on-diff (render → memcmp → reload only on a real diff), so the
// steady-state cost is one cheap NDJSON round-trip, and convergence is
// guaranteed even if a single dispatch fails while the agent is busy — a
// change-triggered call would drop that update forever (feedback_per_tick_
// idempotent_loops). Same precedent as the webmail loop's per-pass
// service.start. Off ⇒ the exempt config isn't composed into the acquis, so
// there is nothing to reload and we skip the agent entirely.
func (r *Reconciler) reconcileBotChallengeExempt(ctx context.Context) {
	domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("bot-exempt reconcile: list domains", "err", err)
		return
	}
	var hosts []string
	for i := range domains {
		if !domains[i].BotChallengeExempt {
			continue
		}
		n := domains[i].Name
		hosts = append(hosts, n, "www."+n)
	}
	if _, err := appseccfg.WriteBotExemptHosts(appseccfg.BotExemptHostsPath, hosts); err != nil {
		r.log.Warn("bot-exempt reconcile: write state file", "err", err)
		// fall through: still try to refresh from whatever is on disk.
	}

	if r.serverSettings == nil {
		return
	}
	s, err := r.serverSettings.Get(ctx)
	if err != nil || s == nil {
		return
	}
	if s.AppSecBotDetection == "" || s.AppSecBotDetection == "off" {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, botExemptAgentTimeout)
	defer cancel()
	if _, err := r.agent.Call(rctx, "security.crowdsec.appsec.botdetection.refresh_exempt", map[string]any{}); err != nil {
		r.log.Warn("bot-exempt reconcile: refresh_exempt", "err", err)
	}
}
