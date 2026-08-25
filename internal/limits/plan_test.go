package limits

import (
	"context"
	"encoding/json"
	"testing"
)

type recPlanAgent struct {
	method string
	params map[string]any
	err    error
}

func (a *recPlanAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.method = method
	a.params, _ = params.(map[string]any)
	return json.RawMessage(`{}`), a.err
}

func pkgLimits() *PackageLimits {
	return &PackageLimits{DiskQuotaMB: 100, CPUQuotaPercent: 50, MemoryLimitMB: 512, IOReadMbps: 20, IOWriteMbps: 20, MaxTasks: 200}
}

// The four "cases agree" scenarios the ticket names, plus the mount gate.
func TestPlanFor(t *testing.T) {
	const mount = "/home"

	// package-only → apply the package values with the (enabled) mount.
	p := PlanFor(pkgLimits(), nil, true, mount)
	if p.Kind != PlanApply || p.Effective.CPUQuotaPercent != 50 || p.QuotaMount != mount {
		t.Fatalf("package-only: %+v", p)
	}

	// override-only → apply the override values.
	ov := &OverrideLimits{CPUQuotaPercent: u32(75)}
	p = PlanFor(nil, ov, true, mount)
	if p.Kind != PlanApply || p.Effective.CPUQuotaPercent != 75 {
		t.Fatalf("override-only: %+v", p)
	}

	// package + override → override field wins, package fills the rest.
	p = PlanFor(pkgLimits(), &OverrideLimits{CPUQuotaPercent: u32(90)}, true, mount)
	if p.Effective.CPUQuotaPercent != 90 || p.Effective.MemoryLimitMB != 512 {
		t.Fatalf("merge: %+v", p.Effective)
	}

	// explicit-zero override + package → the explicit 0 WINS over the package
	// value (pick returns the non-nil override pointer, even when it is 0).
	p = PlanFor(pkgLimits(), &OverrideLimits{CPUQuotaPercent: u32(0)}, true, mount)
	if p.Effective.CPUQuotaPercent != 0 {
		t.Fatalf("explicit-zero override must win, got cpu=%d", p.Effective.CPUQuotaPercent)
	}

	// no package + no override → CLEAR (user must be unlimited).
	p = PlanFor(nil, nil, true, mount)
	if p.Kind != PlanClear {
		t.Fatalf("no-policy must clear, got %+v", p)
	}
}

// Apply gates the mount when the disk-quota feature is disabled; Clear NEVER
// does — removing a stale quota needs the mount regardless of the toggle.
func TestPlanFor_MountGate(t *testing.T) {
	const mount = "/home"

	// Apply with quota disabled → mount blanked (setquota short-circuits).
	if got := PlanFor(pkgLimits(), nil, false, mount); got.Kind != PlanApply || got.QuotaMount != "" {
		t.Fatalf("apply with quota disabled must blank the mount, got %q", got.QuotaMount)
	}
	// Apply with quota enabled → mount carried.
	if got := PlanFor(pkgLimits(), nil, true, mount); got.QuotaMount != mount {
		t.Fatalf("apply with quota enabled must carry the mount, got %q", got.QuotaMount)
	}
	// Clear with quota DISABLED → still carries the raw mount.
	if got := PlanFor(nil, nil, false, mount); got.Kind != PlanClear || got.QuotaMount != mount {
		t.Fatalf("clear must always carry the raw mount even when quota disabled, got %q", got.QuotaMount)
	}
}

func TestLimitsPlan_Execute(t *testing.T) {
	ag := &recPlanAgent{}
	if err := PlanFor(pkgLimits(), nil, true, "/home").Execute(context.Background(), ag, "alice"); err != nil {
		t.Fatalf("apply execute: %v", err)
	}
	if ag.method != "user.limits.apply" || ag.params["username"] != "alice" ||
		ag.params["cpu_quota_percent"] != uint32(50) || ag.params["quota_mount"] != "/home" {
		t.Fatalf("apply wire wrong: method=%s params=%v", ag.method, ag.params)
	}

	ag = &recPlanAgent{}
	if err := PlanFor(nil, nil, false, "/home").Execute(context.Background(), ag, "bob"); err != nil {
		t.Fatalf("clear execute: %v", err)
	}
	if ag.method != "user.limits.clear" || ag.params["username"] != "bob" || ag.params["quota_mount"] != "/home" {
		t.Fatalf("clear wire wrong: method=%s params=%v", ag.method, ag.params)
	}
}
