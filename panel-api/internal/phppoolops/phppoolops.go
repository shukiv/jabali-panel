// Package phppoolops owns the PHP-FPM pool lifecycle logic that the HTTP
// handlers (internal/api) and the operator CLI (cmd/server) both drive:
// the create-side resource caps + FPM dynamic-mode tuning validation, and the
// agent php.pool.apply reconcile.
//
// JAB-360: before this package the CLI carried a hand-copied reconcile
// (cmd/server reconcilePHPPoolCLI) that had already drifted from the HTTP one —
// it omitted the versioned slug/additive (GH #329), the fuller pm.* set, the
// slowlog timeout, extra extensions and Xdebug. A CLI ini mutation on a
// non-default (per-version) pool therefore applied to the user's DEFAULT
// socket. Making both adapters call one module removes the drift by
// construction, and the caps validator closes the parallel gap where CLI
// `create` wrote pm_max_children with no cap and no dynamic-mode constraint.
package phppoolops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Resource caps (GH #339 security): pm_max_children + idle timeout are
// tenant-settable, so they MUST be bounded — an unbounded value is a
// resource-exhaustion DoS on the shared host.
const (
	UserMaxChildrenCap  = 100
	AdminMaxChildrenCap = 2000
	MaxIdleTimeoutSec   = 86400
	MaxRequestsCap      = 100000 // worker recycle bound (0 = disabled)
	MaxTerminateSec     = 3600   // request_terminate_timeout ceiling (0 = disabled)

	defaultMaxChildren = 20
	defaultIdleTimeout = 60
)

// ClampToPackageCap bounds pm_max_children (and dependent spare/start servers)
// to a hosting package's fpm_max_children_cap, then re-runs the FPM dynamic
// constraint via ResolvePMTuning. Load-bearing safety for the L1/L2 user tiers
// (GH #339 phase 2): whatever a non-admin produces can never exceed the cap.
func ClampToPackageCap(cap uint32, mode string, maxChildren, start, minSpare, maxSpare, maxReq, terminate *uint32) (string, bool) {
	if cap > 0 && *maxChildren > cap {
		*maxChildren = cap
	}
	if *start > *maxChildren {
		*start = *maxChildren
	}
	if *maxSpare > *maxChildren {
		*maxSpare = *maxChildren
	}
	return ResolvePMTuning(mode, *maxChildren, start, minSpare, maxSpare, maxReq, terminate)
}

// ResolvePMTuning applies defaults, caps, and the FPM dynamic-mode constraint
// (min_spare <= start <= max_spare <= max_children) to the extended pm.* fields
// (GH #339). It MUTATES the pointed-to values (fills dynamic defaults clamped to
// max_children) and returns a client error + false when invalid. start/spare are
// ignored by FPM outside dynamic mode, so they are only constrained there.
func ResolvePMTuning(mode string, maxChildren uint32, start, minSpare, maxSpare, maxReq, terminate *uint32) (string, bool) {
	if *maxReq > MaxRequestsCap {
		return fmt.Sprintf("pm_max_requests must be <= %d", MaxRequestsCap), false
	}
	if *terminate > MaxTerminateSec {
		return fmt.Sprintf("request_terminate_timeout_seconds must be <= %d", MaxTerminateSec), false
	}
	if mode != "dynamic" {
		return "", true
	}
	if *maxSpare == 0 {
		if maxChildren < 3 {
			*maxSpare = maxChildren
		} else {
			*maxSpare = 3
		}
	}
	if *start == 0 {
		if *maxSpare < 2 {
			*start = *maxSpare
		} else {
			*start = 2
		}
	}
	if *minSpare == 0 {
		*minSpare = 1
		if *minSpare > *start {
			*minSpare = *start
		}
	}
	if !(*minSpare <= *start && *start <= *maxSpare && *maxSpare <= maxChildren) {
		return "dynamic pm requires pm_min_spare_servers <= pm_start_servers <= pm_max_spare_servers <= pm_max_children", false
	}
	return "", true
}

// CreateTuning is the create-side pm.* input/output for one adapter-neutral
// validation pass. Zero fields mean "not provided" (the create convention);
// ResolveCreateTuning fills them with defaults.
type CreateTuning struct {
	PmMode                         string
	PmMaxChildren                  uint32
	ProcessIdleTimeoutSeconds      uint32
	PmStartServers                 uint32
	PmMinSpareServers              uint32
	PmMaxSpareServers              uint32
	PmMaxRequests                  uint32
	RequestTerminateTimeoutSeconds uint32
}

// ResolveCreateTuning applies the create-side defaults, the admin/user
// pm_max_children cap, the idle-timeout ceiling, and the FPM dynamic-mode
// constraint. It returns the resolved values plus a typed client error — a
// message and a stable field key so HTTP maps it to a 400 error code and the
// CLI to a usage error. isAdmin picks the higher pm_max_children cap (the
// operator CLI `create` is admin-scoped, like an admin HTTP create).
//
// This is the single create-side validator both adapters call, so an operator
// CLI create can no longer produce a pool outside the caps and dynamic-mode
// invariants a tenant-facing create enforces (JAB-360 AC 1).
func ResolveCreateTuning(isAdmin bool, in CreateTuning) (out CreateTuning, errMsg, field string, ok bool) {
	out = in
	if out.PmMode == "" {
		out.PmMode = "ondemand"
	} else if out.PmMode != "static" && out.PmMode != "ondemand" && out.PmMode != "dynamic" {
		return out, "pm_mode must be static, ondemand, or dynamic", "invalid_pm_mode", false
	}

	if out.PmMaxChildren == 0 {
		out.PmMaxChildren = defaultMaxChildren
	}
	cap := uint32(UserMaxChildrenCap)
	if isAdmin {
		cap = AdminMaxChildrenCap
	}
	if out.PmMaxChildren > cap {
		return out, fmt.Sprintf("pm_max_children must be <= %d", cap), "pm_max_children_too_high", false
	}

	if out.ProcessIdleTimeoutSeconds == 0 {
		out.ProcessIdleTimeoutSeconds = defaultIdleTimeout
	}
	if out.ProcessIdleTimeoutSeconds > MaxIdleTimeoutSec {
		return out, fmt.Sprintf("process_idle_timeout_seconds must be <= %d", MaxIdleTimeoutSec), "process_idle_timeout_too_high", false
	}

	if msg, valid := ResolvePMTuning(out.PmMode, out.PmMaxChildren,
		&out.PmStartServers, &out.PmMinSpareServers, &out.PmMaxSpareServers,
		&out.PmMaxRequests, &out.RequestTerminateTimeoutSeconds); !valid {
		return out, msg, "pm_tuning_invalid", false
	}
	return out, "", "", true
}

// ReconcileDeps are the repositories + agent ReconcileViaAgent needs. Adapters
// build it from their own DB handle/agent client.
type ReconcileDeps struct {
	Agent     agent.AgentInterface
	Users     repository.UserRepository
	Overrides repository.PHPPoolIniOverrideRepository
	Pools     repository.PHPPoolRepository
	// Packages resolves the user's hosting package so this reconcile can carry
	// the GH #402 per-package disable_functions opt-out (GH #1422). Optional: a
	// nil repo (or a user with no package / a package without the flag) leaves
	// the key off, so the agent applies its #401 command-exec lockdown default.
	// Fail-closed — a missing wiring can only keep the lockdown, never lift it.
	Packages repository.PackageRepository
}

// ReconcileViaAgent fires php.pool.apply for pool, then writes the resulting
// status back to the DB via SetStatus. It resolves the pool's versioned slug
// (GH #329) so a non-default per-version pool applies to its own socket rather
// than the user's default one, and carries the complete pm.* + slowlog +
// extra-extensions + Xdebug model.
//
// pool is taken BY VALUE on purpose: callers that fire it in a goroutine pass a
// copy, so the fire-and-forget reconcile cannot race the handler that keeps
// mutating its own *pool (the repo-wide `-race` flake this consolidation
// removes). Returns the agent error (nil on success) so the CLI can surface
// "apply failed"; HTTP callers run it in a goroutine and read status back from
// the DB.
func ReconcileViaAgent(deps ReconcileDeps, pool models.PHPPool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, err := deps.Users.FindByID(ctx, pool.UserID)
	if err != nil {
		slog.ErrorContext(ctx, "phppoolops.ReconcileViaAgent: load user", "error", err, "pool_id", pool.ID)
		return err
	}

	overridesList, err := deps.Overrides.ListByPool(ctx, pool.ID)
	if err != nil {
		slog.ErrorContext(ctx, "phppoolops.ReconcileViaAgent: list overrides", "error", err, "pool_id", pool.ID)
		return err
	}

	adminValues := []map[string]string{}
	adminFlags := []map[string]string{}
	for _, override := range overridesList {
		kv := map[string]string{"name": override.Directive, "value": override.Value}
		if override.Kind == "flag" {
			adminFlags = append(adminFlags, kv)
		} else {
			adminValues = append(adminValues, kv)
		}
	}

	// GH #329: resolve the pool's slug so a versioned pool applies to its own
	// socket/instance rather than the default per-user one. isDefault = the
	// pool is the user's earliest (created_at ASC).
	username := ""
	if user != nil && user.Username != nil {
		username = *user.Username
	}
	isDefault := true
	if list, lerr := deps.Pools.ListByUserID(ctx, pool.UserID); lerr == nil && len(list) > 0 {
		isDefault = list[0].ID == pool.ID
	}
	slug := models.PoolSlug(username, pool.PHPVersion, isDefault)

	params := map[string]any{
		"username":                          username,
		"slug":                              slug,
		"additive":                          !isDefault,
		"php_version":                       pool.PHPVersion,
		"pm_mode":                           pool.PmMode,
		"pm_max_children":                   pool.PmMaxChildren,
		"process_idle_timeout_seconds":      pool.ProcessIdleTimeoutSeconds,
		"pm_start_servers":                  pool.PmStartServers,
		"pm_min_spare_servers":              pool.PmMinSpareServers,
		"pm_max_spare_servers":              pool.PmMaxSpareServers,
		"pm_max_requests":                   pool.PmMaxRequests,
		"request_terminate_timeout_seconds": pool.RequestTerminateTimeoutSeconds,
		"slowlog_timeout_seconds":           pool.SlowlogTimeoutSeconds,
		"admin_values":                      adminValues,
		"admin_flags":                       adminFlags,
		"extra_extensions":                  []string(pool.ExtraExtensions),
		"xdebug_enabled":                    pool.XdebugEnabled,
	}

	// GH #1422: carry the GH #402 per-package disable_functions opt-out on this
	// path too. Without it, any pool reconcile driven from here (a PHP settings
	// save, a version assign, an Xdebug/extension/tuning change) re-applied the
	// #401 command-exec lockdown and silently re-broke shell_exec/proc_open for
	// an exec-enabled package. Mirror reconciler.applyPHPPool: send "" only when
	// the user's package opts out; omit the key otherwise so the agent keeps its
	// safe default. Fail-closed — nil Packages / no package / flag off => no key.
	if deps.Packages != nil && user != nil && user.PackageID != nil && *user.PackageID != "" {
		if pkg, perr := deps.Packages.FindByID(ctx, *user.PackageID); perr == nil && pkg != nil && pkg.PHPExecEnabled {
			params["disable_functions"] = ""
		}
	}

	_, aerr := deps.Agent.Call(ctx, "php.pool.apply", params)
	if aerr != nil {
		pool.Status = "error"
		msg := fmt.Sprintf("agent failed: %v", aerr)
		pool.LastError = &msg
		_ = deps.Pools.Update(ctx, &pool)
		return aerr
	}
	pool.Status = "ready"
	pool.LastError = nil
	_ = deps.Pools.Update(ctx, &pool)
	return nil
}
