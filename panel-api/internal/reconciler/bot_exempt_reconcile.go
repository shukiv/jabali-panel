package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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
	var exempt, include []string
	for i := range domains {
		n := domains[i].Name
		if domains[i].BotChallengeExempt {
			exempt = append(exempt, n, "www."+n)
		}
		if domains[i].BotChallengeInclude {
			include = append(include, n, "www."+n)
		}
	}
	if _, err := appseccfg.WriteBotExemptHosts(appseccfg.BotExemptHostsPath, exempt); err != nil {
		r.log.Warn("bot-exempt reconcile: write exempt state file", "err", err)
		// fall through: still try to refresh from whatever is on disk.
	}
	// GH #2328: per-path opt-outs. A failed listing keeps the file as it is.
	if r.botChallengeExclusions != nil {
		if rows, err := r.botChallengeExclusions.List(ctx); err != nil {
			r.log.Warn("bot-exempt reconcile: list path exclusions", "err", err)
		} else if _, err := appseccfg.WriteBotExemptPaths(appseccfg.BotExemptPathsPath, botPathExclusionsFor(domains, rows)); err != nil {
			r.log.Warn("bot-exempt reconcile: write path state file", "err", err)
		}
	}
	if _, err := appseccfg.WriteBotIncludeHosts(appseccfg.BotIncludeHostsPath, include); err != nil {
		r.log.Warn("bot-exempt reconcile: write include state file", "err", err)
	}

	if r.serverSettings == nil {
		return
	}
	s, err := r.settingsGet(ctx)
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

// botPathExclusionsFor expands the per-path opt-out rows (GH #2328) to one
// state-file entry per host, the domain and www.<domain>, as the whole-domain
// opt-out does. Rows of a domain missing from domains are left out.
func botPathExclusionsFor(domains []models.Domain, rows []models.BotChallengeExclusion) []appseccfg.BotPathExclusion {
	names := make(map[string]string, len(domains))
	for i := range domains {
		names[domains[i].ID] = strings.ToLower(domains[i].Name)
	}
	out := []appseccfg.BotPathExclusion{}
	for _, row := range rows {
		name, ok := names[row.DomainID]
		if !ok {
			continue
		}
		for _, host := range []string{name, "www." + name} {
			out = append(out, appseccfg.BotPathExclusion{Host: host, PathPrefix: row.PathPrefix, Methods: row.MethodList()})
		}
	}
	return out
}
