// M13.1.1 — bandwidth quota auto-suspend.
//
// When server_settings.bandwidth_quota_enforce_enabled is true:
//   - User's month-to-date bytes ≥ BandwidthQuotaMB → for every owned
//     domain, set is_enabled=false + is_quota_suspended=true.
//   - User's bytes drop ≤ 80% of quota AND any owned domain has
//     is_quota_suspended=true → set is_enabled=true +
//     is_quota_suspended=false on those domains.
//
// is_quota_suspended is the disambiguator: manual admin disables
// (is_enabled=false, is_quota_suspended=false) are NEVER touched by
// this loop. Only panel-driven suspensions get auto-restored.
//
// State transitions emit M14 notifications; envelope kinds reuse the
// already-shipped bandwidth.quota.warn/.crit. The notification path
// is best-effort (Queue may be nil on early-boot tests).
package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	bwSuspendCritPercent = 100.0
	bwSuspendWarnPercent = 80.0

	// bwEnforceInterval bounds how often the suspension sweep runs. bw_daily
	// is written once a day, so anything faster is recomputing a verdict that
	// cannot have changed.
	bwEnforceInterval = time.Hour
)

// reconcileBandwidthQuotaEnforce is the per-tick suspension loop.
// Cheap noop when the toggle is off OR any required dep is nil.
func (r *Reconciler) reconcileBandwidthQuotaEnforce(ctx context.Context) {
	if r.serverSettings == nil || r.users == nil || r.domains == nil ||
		r.bwDaily == nil || r.packages == nil {
		return
	}
	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil || !srv.BandwidthQuotaEnforceEnabled {
		return
	}

	// Evaluate hourly, not every tick. The input (bw_daily) is written once a
	// day by the bandwidth ticker, so 1439 of every 1440 evaluations recompute
	// an identical answer — and each one costs, per eligible user, a package
	// lookup, a join+SUM over month-to-date bandwidth, and a domain list.
	// Suspension still lands within an hour of the daily rollup, which is the
	// only moment the verdict can actually change.
	r.bwEnforceMu.Lock()
	if !r.bwEnforceLastRun.IsZero() && time.Since(r.bwEnforceLastRun) < bwEnforceInterval {
		r.bwEnforceMu.Unlock()
		return
	}
	r.bwEnforceLastRun = time.Now()
	r.bwEnforceMu.Unlock()

	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("bandwidth_quota_enforce: list users failed", "err", err)
		return
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	pkgByID := make(map[string]*models.HostingPackage)

	for _, u := range users {
		if u.IsAdmin || u.PackageID == nil || *u.PackageID == "" {
			continue
		}
		// Package rows are a small static table and many users share one, so
		// resolve each distinct id once per pass instead of once per user.
		pkg, ok := pkgByID[*u.PackageID]
		if !ok {
			p, perr := r.packages.FindByID(ctx, *u.PackageID)
			if perr != nil || p == nil {
				pkgByID[*u.PackageID] = nil
				continue
			}
			pkgByID[*u.PackageID] = p
			pkg = p
		}
		if pkg == nil || pkg.BandwidthQuotaMB == 0 {
			continue
		}
		bytesByDomain, err := r.bwDaily.SumByDomainForUser(ctx, u.ID, monthStart, now)
		if err != nil {
			continue
		}
		var total uint64
		for _, v := range bytesByDomain {
			total += v
		}
		quotaBytes := uint64(pkg.BandwidthQuotaMB) * 1024 * 1024
		if quotaBytes == 0 {
			continue
		}
		pct := float64(total) / float64(quotaBytes) * 100.0

		userDomains, _, err := r.domains.ListByUserID(ctx, u.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			continue
		}

		switch {
		case pct >= bwSuspendCritPercent:
			r.suspendUserDomains(ctx, u.ID, userDomains, total, quotaBytes)
		case pct <= bwSuspendWarnPercent:
			r.unsuspendUserDomains(ctx, u.ID, userDomains, total, quotaBytes)
		}
	}
}

func (r *Reconciler) suspendUserDomains(ctx context.Context, userID string, userDomains []models.Domain, total, quotaBytes uint64) {
	count := 0
	for i := range userDomains {
		d := &userDomains[i]
		if d.IsQuotaSuspended {
			continue
		}
		d.IsEnabled = false
		d.IsQuotaSuspended = true
		d.UpdatedAt = time.Now().UTC()
		if err := r.domains.Update(ctx, d); err != nil {
			r.log.Warn("bandwidth_quota_enforce: suspend update failed", "domain_id", d.ID, "err", err)
			continue
		}
		count++
		r.Schedule(d.ID) // reconciler push tears down nginx vhost
	}
	if count > 0 {
		r.log.Info("bandwidth_quota_enforce: suspended user", "user_id", userID, "domains", count)
		r.publishQuotaTransition(ctx, userID, total, quotaBytes, count, "bandwidth.quota.crit", "critical",
			"User domains suspended due to bandwidth quota", "domains_suspended")
	}
}

func (r *Reconciler) unsuspendUserDomains(ctx context.Context, userID string, userDomains []models.Domain, total, quotaBytes uint64) {
	count := 0
	for i := range userDomains {
		d := &userDomains[i]
		if !d.IsQuotaSuspended {
			continue
		}
		d.IsEnabled = true
		d.IsQuotaSuspended = false
		d.UpdatedAt = time.Now().UTC()
		if err := r.domains.Update(ctx, d); err != nil {
			r.log.Warn("bandwidth_quota_enforce: restore update failed", "domain_id", d.ID, "err", err)
			continue
		}
		count++
		r.Schedule(d.ID)
	}
	if count > 0 {
		r.log.Info("bandwidth_quota_enforce: restored user", "user_id", userID, "domains", count)
		r.publishQuotaTransition(ctx, userID, total, quotaBytes, count, "bandwidth.quota.warn", "info",
			"User domains restored after bandwidth dropped below 80%", "domains_restored")
	}
}

func (r *Reconciler) publishQuotaTransition(ctx context.Context, userID string, total, quotaBytes uint64, domainCount int, kind, sev, title, body string) {
	if r.notificationQueue == nil {
		return
	}
	_, _ = r.notificationQueue.Publish(ctx, notifications.Envelope{
		EventKind: kind,
		Severity:  sev,
		UserID:    userID,
		Title:     title,
		Body:      body,
		Deeplink:  "/jabali-admin/users/" + userID,
	})
}
