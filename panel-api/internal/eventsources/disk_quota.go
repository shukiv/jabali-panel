package eventsources

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	diskQuotaTick    = 30 * time.Minute
	diskQuotaCoolOff = 6 * time.Hour
	// Fire when used / hard ≥ 90%. Hard cap is the "you can't write
	// any more" boundary; warning before that gives the user time to
	// clean up before write failures hit.
	diskQuotaPercent = 90.0

	// diskQuotaMaxAge is the freshness ceiling for a persisted snapshot.
	// The disk-usage sweeper (internal/diskusagesweeper) refreshes
	// disk_checked_at every 15 min, but a full pass on a ~5,000-account
	// host spends ~21 min in inter-user pacing alone before command and
	// DB time, so a snapshot can legitimately trail more than one
	// interval. 2h clears a lagging sweep yet still refuses one that is
	// wedged (agent down, disk hung) — and it sits well under the 6h
	// per-user cooloff, so a genuinely near-full account is never
	// silenced by the age gate between alerts (JAB-376 AC #7).
	diskQuotaMaxAge = 2 * time.Hour
)

// runDiskQuota iterates every hosting user (those with a non-empty
// username, i.e. excluded admins) every 30 minutes and fires a
// per-user envelope when used / hard ≥ 90%. 6-hour cooldown per user
// so a chronically-near-full account doesn't spam.
//
// It reads the disk figures the disk-usage sweeper already persists
// on each user row (disk_used_kb / disk_limit_kb / disk_checked_at)
// rather than re-polling the agent per user: the sweeper measures the
// same POSIX quota state every 15 minutes, so a second per-user
// user.limits.report loop here was ~2,000 redundant Agent calls per
// hour on a 1,000-account host (JAB-376 AC #3 — zero Agent calls). A
// snapshot older than diskQuotaMaxAge is refused rather than alerted
// on (AC #7).
//
// No-op when Users or QuotaMount aren't wired (CI/dev boxes without
// /home as a separate fs never populate a meaningful snapshot).
func runDiskQuota(ctx context.Context, d Deps) {
	if d.Users == nil || d.QuotaMount == "" {
		d.Log.Debug("eventsources: disk_quota disabled — missing Users/QuotaMount")
		return
	}
	// One-shot at boot. Note: a fresh install has no snapshot yet, so
	// the first meaningful pass is after the sweeper's first sweep
	// (~15 min) rather than an immediate live poll — deliberate, per
	// AC #7 (never alert on data we haven't observed).
	diskQuotaPass(ctx, d)
	tick := time.NewTicker(diskQuotaTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		diskQuotaPass(ctx, d)
	}
}

func diskQuotaPass(ctx context.Context, d Deps) {
	users, _, err := d.Users.List(ctx, repository.ListOptions{Limit: 5000})
	if err != nil {
		d.Log.Warn("eventsources: disk_quota list users failed", "err", err)
		return
	}
	now := d.Now()
	for i := range users {
		u := &users[i]
		if u.Username == nil || *u.Username == "" {
			continue
		}
		// AC #7: a stale or failed sweep leaves an old disk_checked_at
		// (or nil for never-swept). Refuse to alert on a figure that may
		// no longer hold rather than firing on last-good data past the
		// freshness ceiling.
		if u.DiskCheckedAt == nil {
			continue // never swept — no observation yet
		}
		if now.Sub(*u.DiskCheckedAt) > diskQuotaMaxAge {
			continue // sweeper lagging/wedged — last-good too old to alert on
		}
		if u.DiskLimitKB == 0 {
			continue // unlimited or unconfigured — nothing to alert on
		}
		pct := float64(u.DiskUsedKB) / float64(u.DiskLimitKB) * 100.0
		if pct < diskQuotaPercent {
			continue
		}
		tag := "user:" + *u.Username
		if !shouldFire(ctx, d, "disk.quota.warn", tag, diskQuotaCoolOff) {
			continue
		}
		_, err = d.Queue.Publish(ctx, notifications.Envelope{
			EventKind: "disk.quota.warn",
			Severity:  models.NotificationSeverityWarning,
			Title:     fmt.Sprintf("%s at %.0f%% of disk quota", *u.Username, pct),
			Body: fmt.Sprintf(
				"User %s used %d KB of %d KB hard limit (%.1f%%). (%s)",
				*u.Username, u.DiskUsedKB, u.DiskLimitKB, pct, tag,
			),
			Deeplink: "/jabali-admin/users",
			UserID:   u.ID,
		})
		if err != nil {
			d.Log.Warn("eventsources: publish disk.quota.warn failed", "user", *u.Username, "err", err)
		}
	}
}
