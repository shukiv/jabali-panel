package hostedsvc

import (
	"context"
	"log/slog"
	"time"
)

// ReclaimMoved removes DNS and revokes every label whose box left its source
// IP more than reclaimAfterMove ago and never came back. It closes the
// dangling-A-record tail a move leaves behind: once the box re-claims at its
// new address, the old `<label>.<base> A <old-ip>` record points at an address
// the fleet no longer controls — and which the provider may reassign to a
// stranger — yet still carries Let's Encrypt issuance rights for that hostname.
// Run from the `reap` subcommand on a daily root cron.
//
// Per-label ordering is deliberate and follows the per-tick-idempotent rule:
// DNS removal must SUCCEED before the row is revoked. A revoked row drops out
// of MovedLabelsBefore forever, so revoking after a failed DNS delete (a
// transient Cloudflare outage, say) would strand the exact record this exists
// to remove. On DNS failure we log, leave moved_at set, and let the next tick
// retry. This is why we do NOT copy the release handler's log-and-proceed model
// — release is a user-initiated one-shot; the reaper is a repeating tick.
//
// dryRun reports the worklist without touching DNS or the store — the first
// production invocation should run with it.
//
// Known scope hole (by design): a box that re-claims at its new IP WITHOUT
// first sending a heartbeat on the old token never gets moved_at stamped, so
// this reaper can't see that orphan. The client drives its re-claim off the
// ip_moved heartbeat response, so in the normal flow the detecting heartbeat
// stamps moved_at first and the gap is only the out-of-band re-claim path.
// Reclaiming purely-idle boxes by stale last_seen is a different policy with
// different risk and is intentionally NOT done here.
func ReclaimMoved(ctx context.Context, store *Store, dns DNSBackend, log *slog.Logger, now time.Time, dryRun bool) (reaped int, err error) {
	cutoff := now.Add(-reclaimAfterMove)
	labels, err := store.MovedLabelsBefore(cutoff)
	if err != nil {
		return 0, err
	}
	for _, label := range labels {
		if dryRun {
			log.Info("reap: would reclaim", "label", label, "fqdn", FQDN(label))
			reaped++
			continue
		}
		if err := dns.RemoveLabel(ctx, label); err != nil {
			// Leave moved_at set so the next tick retries. Never revoke a
			// label whose DNS we failed to remove — that would strand its A
			// record for good, the opposite of this reaper's job.
			log.Error("reap: dns remove failed, leaving for next run", "label", label, "err", err)
			continue
		}
		if err := store.ReclaimLabel(label); err != nil {
			log.Error("reap: reclaim (revoke) failed", "label", label, "err", err)
			continue
		}
		log.Info("reap: reclaimed", "label", label, "fqdn", FQDN(label))
		reaped++
	}
	return reaped, nil
}
