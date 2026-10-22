package eventsources

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// egressBurstTick is how often the source polls per-user drop counts.
// Reconciler updates drop_count_24h every ~60s, so anything finer wastes
// cycles; matching the reconciler cadence is the natural choice.
const egressBurstTick = 60 * time.Second

// egressBurstCoolOff debounces per-user fires so a sustained webshell
// doesn't drown the operator's M14 channel. Same window as crowdsec_spike
// — long enough that a real attack still produces follow-up alerts every
// 15 min, short enough that a fresh burst after dawn registers as a new
// event, not a continuation.
const egressBurstCoolOff = 15 * time.Minute

// runEgressBurst polls user_egress_policies every minute and fires
// egress.drop.burst whenever a user's drop_count_24h (which the
// reconciler currently fills with per-tick deltas) crosses the
// operator-tunable threshold (server_settings.egress_burst_threshold,
// default 50). The count is the user's traffic outside the egress
// allowlist: dropped packets when the policy is enforced, new connections
// that were allowed and logged when it is learning. The alert says which.
// Disabled when either repo is unwired.
func runEgressBurst(ctx context.Context, d Deps) {
	if d.UserEgressPolicies == nil || d.ServerSettings == nil {
		d.Log.Info("eventsources: egress.drop.burst disabled (missing repos)")
		return
	}
	d.Log.Info("eventsources: egress.drop.burst started", "tick", egressBurstTick.String())
	egressBurstPass(ctx, d)
	tick := time.NewTicker(egressBurstTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		egressBurstPass(ctx, d)
	}
}

func egressBurstPass(ctx context.Context, d Deps) {
	settings, err := d.ServerSettings.Get(ctx)
	if err != nil || settings == nil {
		d.Log.Debug("egress.drop.burst: server_settings unavailable", "err", err)
		return
	}
	threshold := uint64(settings.EgressBurstThreshold)
	if threshold == 0 {
		threshold = 50
	}

	policies, err := d.UserEgressPolicies.List(ctx)
	if err != nil {
		d.Log.Debug("egress.drop.burst: list policies failed", "err", err)
		return
	}

	for _, p := range policies {
		if p.DropCount24h < threshold {
			continue
		}
		dedupe := fmt.Sprintf("user=%s drops>=%d", p.UserID, threshold)
		if !shouldFire(ctx, d, "egress.drop.burst", dedupe, egressBurstCoolOff) {
			continue
		}
		title, body := egressBurstText(p.State, egressBurstUserName(ctx, d, p.UserID), p.DropCount24h, threshold)
		_, err := d.Queue.Publish(ctx, notifications.Envelope{
			EventKind: "egress.drop.burst",
			Severity:  models.NotificationSeverityWarning,
			Title:     title,
			// shouldFire matches the dedupe tag in the body, so the tag
			// rides along at the end (as crowdsec.ban.spike does).
			Body:     fmt.Sprintf("%s (%s)", body, dedupe),
			Deeplink: "/admin/security?tab=egress",
		})
		if err != nil {
			d.Log.Warn("egress.drop.burst: publish failed", "err", err, "user_id", p.UserID)
		}
	}
}

// egressBurstText words the alert for the policy state. A learning policy
// allows the traffic, so its alert never says the traffic was blocked.
func egressBurstText(state, user string, n, threshold uint64) (title, body string) {
	if state == models.UserEgressStateLearning {
		return fmt.Sprintf("Egress learning: %s made %d new connections outside the allowlist", user, n),
			fmt.Sprintf(
				"User %s is in learning mode: %d new outbound connections outside the egress allowlist "+
					"since the previous check, about a minute (threshold %d), were allowed and logged. "+
					"Enforced mode would block them. Review the destinations in the user's Egress drops "+
					"list and allow the ones their apps need before the policy is enforced.",
				user, n, threshold,
			)
	}
	return fmt.Sprintf("Egress burst: %s had %d outbound packets blocked", user, n),
		fmt.Sprintf(
			"User %s sent %d outbound packets outside the egress allowlist since the previous check, "+
				"about a minute (threshold %d), and the firewall dropped them. Likely a webshell or "+
				"compromised script trying to phone home, or an app that needs a destination allowed. "+
				"The user's Egress drops list shows the destinations.",
			user, n, threshold,
		)
}

// egressBurstUserName resolves the account name for the alert, falling
// back to the user id when the users repo is unwired or the lookup fails.
func egressBurstUserName(ctx context.Context, d Deps, userID string) string {
	if d.Users == nil {
		return userID
	}
	u, err := d.Users.FindByID(ctx, userID)
	if err != nil || u == nil || u.Username == nil || *u.Username == "" {
		return userID
	}
	return *u.Username
}
