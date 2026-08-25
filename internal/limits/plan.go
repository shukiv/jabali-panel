package limits

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentCaller is the minimal agent surface LimitsPlan.Execute needs — satisfied
// structurally by the panel's agent client, so the limits package stays free of
// any dependency on it.
type AgentCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// PlanKind is Apply (converge the effective limits) or Clear (the user must be
// unlimited — remove any drop-in + POSIX quota a previously-assigned package
// left behind).
type PlanKind int

const (
	PlanApply PlanKind = iota
	PlanClear
)

// LimitsPlan is the typed Apply/Clear decision for one user's effective resource
// limits, shared by the Reconciler and the operator CLI so both emit identical
// plans for identical stored state (JAB-309). The REST layer computes the same
// effective limits for its read view via Resolve but does not dispatch.
type LimitsPlan struct {
	Kind      PlanKind
	Effective EffectiveLimits // meaningful only for PlanApply
	// QuotaMount is the setquota mount path passed to the agent. For Apply it is
	// gated by the disk-quota feature toggle ("" when disabled, so the agent's
	// setquota step short-circuits while cgroup limits still apply). For Clear it
	// is ALWAYS the raw mount: removing a POSIX quota needs the mount even when
	// the feature is currently disabled, or a stale quota from a prior package
	// survives on disk — the exact "old quota wall" a clear exists to remove.
	QuotaMount string
}

// PlanFor computes the plan from stored state. No package AND no override → the
// user must be unlimited, so Clear (skipping instead would leave stale quota +
// cgroup drop-ins on the host). Otherwise Apply the resolved effective limits.
func PlanFor(pkg *PackageLimits, override *OverrideLimits, quotaEnabled bool, quotaMount string) LimitsPlan {
	if pkg == nil && override == nil {
		return LimitsPlan{Kind: PlanClear, QuotaMount: quotaMount}
	}
	applyMount := quotaMount
	if !quotaEnabled {
		applyMount = ""
	}
	return LimitsPlan{Kind: PlanApply, Effective: Resolve(pkg, override), QuotaMount: applyMount}
}

// Execute dispatches the plan to the agent: user.limits.apply with the effective
// values, or user.limits.clear. Both handlers are idempotent.
func (p LimitsPlan) Execute(ctx context.Context, ag AgentCaller, username string) error {
	if p.Kind == PlanClear {
		_, err := ag.Call(ctx, "user.limits.clear", map[string]any{
			"username":    username,
			"quota_mount": p.QuotaMount,
		})
		return err
	}
	_, err := ag.Call(ctx, "user.limits.apply", map[string]any{
		"username":          username,
		"disk_quota_mb":     p.Effective.DiskQuotaMB,
		"cpu_quota_percent": p.Effective.CPUQuotaPercent,
		"memory_limit_mb":   p.Effective.MemoryLimitMB,
		"io_read_mbps":      p.Effective.IOReadMbps,
		"io_write_mbps":     p.Effective.IOWriteMbps,
		"max_tasks":         p.Effective.MaxTasks,
		"quota_mount":       p.QuotaMount,
	})
	return err
}

// Describe renders the plan for a dry-run, without dispatching.
func (p LimitsPlan) Describe(username string) string {
	if p.Kind == PlanClear {
		return fmt.Sprintf("clear  %s (no package or override — remove limits)", username)
	}
	return fmt.Sprintf("apply  %s (disk=%dMB cpu=%d%% mem=%dMB io_r=%d io_w=%d tasks=%d quota_mount=%q)",
		username, p.Effective.DiskQuotaMB, p.Effective.CPUQuotaPercent, p.Effective.MemoryLimitMB,
		p.Effective.IOReadMbps, p.Effective.IOWriteMbps, p.Effective.MaxTasks, p.QuotaMount)
}
