package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/limitsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// M18 — per-user resource limits + per-domain nginx rate limits.
//
// Two convergence passes:
//
//   ReconcileUserLimits  — for every user with a package or override,
//     compute the effective limits and call user.limits.apply. The
//     agent is idempotent: a no-change reapply is cheap (compares
//     drop-in content on disk, skips the write) but we still make the
//     RPC every pass for drift detection.
//
//   ReconcileNginxRateLimits — collect every domain with a non-zero
//     rate_limit_rps or connection_limit, send the whole set to
//     nginx.ratelimits.apply which renders the single fragment at
//     /etc/nginx/conf.d/00-jabali-ratelimits.conf.

// WithPackages injects the package repository — required for M18
// ReconcileUserLimits to hydrate the per-user effective limits.
func (r *Reconciler) WithPackages(packages repository.PackageRepository) *Reconciler {
	r.packages = packages
	return r
}

// WithLimitOverrides injects the user-limit-override repository.
func (r *Reconciler) WithLimitOverrides(overrides repository.UserLimitOverrideRepository) *Reconciler {
	r.limitOverrides = overrides
	return r
}

// WithQuotaMount records the filesystem mount path containing /home,
// passed on every user.limits.* agent call so the agent uses an
// explicit `setquota` mount path (never -a).
func (r *Reconciler) WithQuotaMount(mount string) *Reconciler {
	r.quotaMount = mount
	return r
}

// ReconcileUserLimits walks every user in the DB, resolves their
// effective limits, and calls user.limits.apply on the agent. Any
// single-user failure is logged and skipped — this pass must NOT
// short-circuit a whole reconcile cycle for one bad user.
//
// Runs after ReconcileUsers (not explicitly — our existing pipeline
// provisions users on demand during domain reconcile). The net effect
// is the same: every user that has a working Linux account on this
// host gets its limits converged every tick.
//
// Silently no-ops if dependencies aren't wired yet (pre-M18 deployments
// or tests that don't care about this codepath).
func (r *Reconciler) ReconcileUserLimits(ctx context.Context) {
	if r.packages == nil || r.limitOverrides == nil || r.users == nil {
		return
	}

	// Fetch all users — at reasonable host sizes (<10k) this is a single
	// round-trip. For larger deployments we'd paginate.
	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("reconcile user-limits: list users failed", "err", err)
		return
	}

	// Read the global disk-quota toggle once per pass (server_settings.
	// disk_quota_enabled, migration 000071). The shared PlanFor gates it into
	// the wire mount: Apply blanks the mount when disabled (setquota
	// short-circuits, cgroup limits still apply); Clear always carries the raw
	// mount so a stale quota can still be removed.
	quotaEnabled := true
	if r.serverSettings != nil {
		if s, sErr := r.settingsGet(ctx); sErr == nil && s != nil {
			quotaEnabled = s.DiskQuotaEnabled
		}
	}

	// Batch-load overrides in one query to avoid N+1 lookups.
	ovAll, err := r.limitOverrides.ListAll(ctx)
	if err != nil {
		r.log.Warn("reconcile user-limits: list overrides failed", "err", err)
		ovAll = nil
	}
	overridesByUser := make(map[string]*limits.OverrideLimits, len(ovAll))
	for i := range ovAll {
		o := ovAll[i]
		overridesByUser[o.UserID] = &limits.OverrideLimits{
			DiskQuotaMB:     o.DiskQuotaMB,
			CPUQuotaPercent: o.CPUQuotaPercent,
			MemoryLimitMB:   o.MemoryLimitMB,
			IOReadMbps:      o.IOReadMbps,
			IOWriteMbps:     o.IOWriteMbps,
			MaxTasks:        o.MaxTasks,
		}
	}

	// Isolated FTP/SFTP accounts hold shares of their tenant's disk quota
	// (own uid, own quota), so the tenant's own limit is the rest. Without
	// the shares a tenant would get its whole quota back: skip the pass.
	var shares map[string]uint64
	if r.ftpAccounts != nil {
		if shares, err = r.ftpAccounts.SumIsolatedQuotaByUser(ctx); err != nil {
			r.log.Warn("reconcile user-limits: isolated FTP account quotas unavailable; pass skipped", "err", err)
			return
		}
	}
	// What tenants with shares use on the host now, read once and before any
	// limit changes: a tenant already above its share keeps what it uses
	// (limits.TenantDiskQuotaMB).
	var disks map[string]limits.HostDisk
	var disksErr error
	splitOnHost := quotaEnabled && r.quotaMount != ""
	if len(shares) > 0 && splitOnHost {
		ctxInv, cancel := context.WithTimeout(ctx, 30*time.Second)
		disks, disksErr = limits.ReadHostDisks(ctxInv, r.agent, r.quotaMount)
		cancel()
	}

	for i := range users {
		u := &users[i]
		if u.Username == nil || *u.Username == "" || u.IsAdmin {
			continue // no Linux account yet — skip, will pick up next pass
		}
		share := limits.IsolatedShares{SharesMB: shares[u.ID]}
		diskUnknown := false
		if share.SharesMB > 0 && splitOnHost {
			if d, ok := limits.HostDiskOf(disks, disksErr, *u.Username); ok {
				share.Host = &d
			} else {
				// Its disk limit could cut the tenant off without the
				// reading: that stays as it is until the host answers.
				diskUnknown = true
				r.log.Warn("reconcile user-limits: no host quota reading for a tenant with isolated accounts; its disk limit is left as is this pass",
					"username", *u.Username, "err", disksErr)
			}
		}

		var pkgL *limits.PackageLimits
		if u.PackageID != nil && *u.PackageID != "" {
			pkg, err := r.packages.FindByID(ctx, *u.PackageID)
			if err == nil {
				pkgL = &limits.PackageLimits{
					DiskQuotaMB:     pkg.DiskQuotaMB,
					CPUQuotaPercent: pkg.CPUQuotaPercent,
					MemoryLimitMB:   pkg.MemoryLimitMB,
					IOReadMbps:      pkg.IOReadMbps,
					IOWriteMbps:     pkg.IOWriteMbps,
					MaxTasks:        pkg.MaxTasks,
				}
			}
		}
		// Compute + dispatch the shared Apply/Clear plan (JAB-309). No package
		// AND no override → Clear (the user must be unlimited); skipping would
		// leave stale POSIX quotas + cgroup drop-ins from a previously-assigned
		// package, so the user keeps hitting the old quota wall. Both agent
		// handlers are idempotent.
		plan := limits.PlanFor(pkgL, overridesByUser[u.ID], quotaEnabled, r.quotaMount, share)
		if diskUnknown && plan.Kind == limits.PlanApply {
			plan.QuotaMount = "" // the agent skips setquota; the cgroup limits still apply
		}
		unlock, ok := r.holdTenantShare(ctx, u.ID, *u.Username, shares[u.ID])
		if !ok {
			continue
		}
		ctxCall, cancel := context.WithTimeout(ctx, 10*time.Second)
		if mismatch, err := plan.Execute(ctxCall, r.agent, *u.Username); err != nil {
			r.log.Warn("reconcile user-limits: dispatch failed",
				"username", *u.Username, "plan", plan.Describe(*u.Username), "err", err)
		} else if mismatch != "" {
			r.log.Warn("reconcile user-limits: running slice still differs",
				"username", *u.Username, "plan", plan.Describe(*u.Username), "kernel", mismatch)
		}
		cancel()
		unlock()
	}
}

// holdTenantShare locks the tenant's limits against an isolated FTP/SFTP
// account's create or delete, which set the tenant's share and limit
// themselves (limitsync.LockTenant), and checks the share read at the start of
// the pass still holds. ok is false, and the tenant is left for the next pass,
// while such a change is under way or after one changed the share: the plan
// would put back the limit of the old share.
func (r *Reconciler) holdTenantShare(ctx context.Context, userID, username string, sharesMB uint64) (unlock func(), ok bool) {
	unlock, ok = limitsync.TryLockTenant(userID)
	if !ok {
		r.log.Debug("reconcile user-limits: an FTP account change is under way; left for the next pass", "username", username)
		return nil, false
	}
	if r.ftpAccounts == nil {
		return unlock, true
	}
	now, err := r.ftpAccounts.SumIsolatedQuotaByUserID(ctx, userID)
	switch {
	case err != nil:
		r.log.Warn("reconcile user-limits: isolated FTP account quotas unavailable; left for the next pass", "username", username, "err", err)
	case now != sharesMB:
		r.log.Debug("reconcile user-limits: isolated FTP accounts changed during the pass; left for the next pass", "username", username)
	default:
		return unlock, true
	}
	unlock()
	return nil, false
}

// ReconcileNginxRateLimits renders the shared zone-declaration fragment
// at /etc/nginx/conf.d/00-jabali-ratelimits.conf by walking every
// domain with a non-zero rate_limit_rps or connection_limit and sending
// the bundle to nginx.ratelimits.apply on the agent.
//
// Per-vhost `limit_req` / `limit_conn` directives are emitted when
// domain.create renders the vhost (using BuildRateLimitDirectives from
// the agent package). That path is already on every domain convergence
// tick so no separate call is needed here — the fragment is the ONLY
// thing centralised at the reconciler level.
func (r *Reconciler) ReconcileNginxRateLimits(ctx context.Context) {
	if r.domains == nil {
		return
	}
	allDomains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("reconcile nginx-ratelimits: list domains failed", "err", err)
		return
	}

	// Build the slice of domains with any non-zero limit — the agent
	// renders only what it's given (empty input = empty fragment, which
	// is a valid no-op file).
	type rateDomain struct {
		DomainID        string `json:"domain_id"`
		RateLimitRPS    uint32 `json:"rate_limit_rps"`
		ConnectionLimit uint32 `json:"connection_limit"`
	}
	bundle := make([]rateDomain, 0, len(allDomains))
	for i := range allDomains {
		d := &allDomains[i]
		if d.RateLimitRPS == 0 && d.ConnectionLimit == 0 {
			continue
		}
		bundle = append(bundle, rateDomain{
			DomainID:        d.ID,
			RateLimitRPS:    d.RateLimitRPS,
			ConnectionLimit: d.ConnectionLimit,
		})
	}

	// JAB-369: the fragment is a pure function of this bundle, so an
	// unchanged bundle is skipped until the audit interval. Sorted so the
	// fingerprint does not depend on the list order (the Agent sorts too).
	sort.Slice(bundle, func(i, j int) bool { return bundle[i].DomainID < bundle[j].DomainID })
	params := map[string]any{
		"domains":      bundle,
		"zone_size_kb": 0, // 0 -> agent default (10 MB)
	}
	// The two calls in one tick keep their ordering role. On 0→N the first
	// call declares the zone before the domain loop writes a vhost that
	// references it; it succeeds and is stamped, so the second call skips.
	// On N→0 the first call fails: the Agent's nginx -t still sees the old
	// vhost referencing the zone and rolls the fragment back. A failure is
	// never stamped, so after the domain loop re-renders the vhost the
	// second call applies again and succeeds.
	_, _ = r.project(ctx, PhaseNginxRateLimits, "fragment", fingerprint(params), false, func() error {
		return r.applyNginxRateLimits(ctx, params, len(bundle))
	})
}

// applyNginxRateLimits sends the zone fragment and reloads nginx when the
// Agent changed it.
func (r *Reconciler) applyNginxRateLimits(ctx context.Context, params map[string]any, count int) error {
	ctxCall, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := r.agent.Call(ctxCall, "nginx.ratelimits.apply", params)
	if err != nil {
		r.log.Warn("reconcile nginx-ratelimits: apply failed", "err", err, "count", count)
		return err
	}
	// Reload nginx ONLY when the agent's idempotent compare flipped
	// the fragment. Without this gate, every tick with at least one
	// rate-limited domain reloaded nginx (puzzle saw 181 reloads/hr
	// -- Lua state reinit on each, ~340 MB PSS as old workers
	// drained). The agent's nginx.ratelimits.apply already returns
	// no_change=true when the rendered fragment matches the live
	// file; trust that signal.
	var resp struct {
		NoChange bool `json:"no_change"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		r.log.Debug("reconcile nginx-ratelimits: missing no_change in agent response; reloading defensively", "err", jerr)
	} else if resp.NoChange {
		return nil
	}
	if count == 0 {
		return nil
	}
	reloadCtx, reloadCancel := context.WithTimeout(ctx, 10*time.Second)
	defer reloadCancel()
	if _, err := r.agent.Call(reloadCtx, "nginx.reload", nil); err != nil {
		r.log.Warn("reconcile nginx-ratelimits: reload failed", "err", err)
		return err
	}
	return nil
}

// Ensure any struct field additions here show up as compile errors
// in the main reconciler file — the Reconciler struct has the real
// type declarations and this file's helpers assume those fields exist.
var _ = fmt.Sprintf // keep fmt import stable even if future edits drop all its uses
