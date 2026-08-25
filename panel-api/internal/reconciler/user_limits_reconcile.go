package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
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
		if s, sErr := r.serverSettings.Get(ctx); sErr == nil && s != nil {
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

	for i := range users {
		u := &users[i]
		if u.Username == nil || *u.Username == "" || u.IsAdmin {
			continue // no Linux account yet — skip, will pick up next pass
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
		plan := limits.PlanFor(pkgL, overridesByUser[u.ID], quotaEnabled, r.quotaMount)
		ctxCall, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := plan.Execute(ctxCall, r.agent, *u.Username); err != nil {
			r.log.Warn("reconcile user-limits: dispatch failed",
				"username", *u.Username, "plan", plan.Describe(*u.Username), "err", err)
		}
		cancel()
	}
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

	ctxCall, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := r.agent.Call(ctxCall, "nginx.ratelimits.apply", map[string]any{
		"domains":      bundle,
		"zone_size_kb": 0, // 0 -> agent default (10 MB)
	})
	if err != nil {
		r.log.Warn("reconcile nginx-ratelimits: apply failed", "err", err, "count", len(bundle))
		return
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
		return
	}
	if len(bundle) == 0 {
		return
	}
	reloadCtx, reloadCancel := context.WithTimeout(ctx, 10*time.Second)
	defer reloadCancel()
	if _, err := r.agent.Call(reloadCtx, "nginx.reload", nil); err != nil {
		r.log.Warn("reconcile nginx-ratelimits: reload failed", "err", err)
	}
}

// Ensure any struct field additions here show up as compile errors
// in the main reconciler file — the Reconciler struct has the real
// type declarations and this file's helpers assume those fields exist.
var _ = fmt.Sprintf // keep fmt import stable even if future edits drop all its uses
