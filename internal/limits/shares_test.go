package limits

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func mb(n uint64) uint64 { return n * 1024 }

func TestTenantDiskQuotaMB(t *testing.T) {
	plan := func(quota uint32, shares uint64, host *HostDisk) LimitsPlan {
		return LimitsPlan{Kind: PlanApply, Effective: EffectiveLimits{DiskQuotaMB: quota}, Shares: IsolatedShares{SharesMB: shares, Host: host}}
	}
	for _, tc := range []struct {
		name   string
		p      LimitsPlan
		wantMB uint32
	}{
		{"no quota: nothing to share", plan(0, 300, nil), 0},
		{"no shares: the whole quota", plan(1000, 0, &HostDisk{UsedKB: mb(900)}), 1000},
		{"the quota less the shares", plan(1000, 300, nil), 700},
		{"shares take it all: never 0 (no limit)", plan(1000, 1000, nil), 1},
		{"shares above the quota: never 0", plan(1000, 1500, nil), 1},
		{"under the share: the share", plan(1000, 300, &HostDisk{UsedKB: mb(100), LimitKB: mb(1000)}), 700},
		{"over the share at the first apply: usage plus headroom", plan(1000, 300, &HostDisk{UsedKB: mb(900), LimitKB: mb(1000)}), 900 + GrandfatherHeadroomMB},
		{"kept figure doesn't grow with usage", plan(1000, 300, &HostDisk{UsedKB: mb(960), LimitKB: mb(964)}), 964},
		{"kept figure drops as space is freed", plan(1000, 300, &HostDisk{UsedKB: mb(800), LimitKB: mb(964)}), 864},
		{"down to the share", plan(1000, 300, &HostDisk{UsedKB: mb(500), LimitKB: mb(864)}), 700},
		{"no limit on the host counts as the quota", plan(1000, 300, &HostDisk{UsedKB: mb(900)}), 964},
		{"never above the quota", plan(1000, 300, &HostDisk{UsedKB: mb(990), LimitKB: mb(1000)}), 1000},
		{"a lower quota applies in full", plan(800, 300, &HostDisk{UsedKB: mb(900), LimitKB: mb(1000)}), 800},
		{"a partial MB of usage rounds up", plan(1000, 300, &HostDisk{UsedKB: mb(900) + 1, LimitKB: mb(1000)}), 901 + GrandfatherHeadroomMB},
	} {
		if got := tc.p.TenantDiskQuotaMB(); got != tc.wantMB {
			t.Errorf("%s: %d MB, want %d", tc.name, got, tc.wantMB)
		}
	}
}

func TestLimitsPlan_ExecuteSendsTheTenantsOwnLimit(t *testing.T) {
	ag := &recPlanAgent{}
	p := PlanFor(pkgLimits(), nil, true, "/home", IsolatedShares{SharesMB: 30})
	if err := p.Execute(context.Background(), ag, "alice"); err != nil {
		t.Fatal(err)
	}
	if ag.params["disk_quota_mb"] != uint32(70) {
		t.Errorf("disk_quota_mb = %v, want 70 (100 less 30 of shares)", ag.params["disk_quota_mb"])
	}
	if d := p.Describe("alice"); !strings.Contains(d, "disk=70MB of 100MB, 30MB to isolated accounts") {
		t.Errorf("describe = %q", d)
	}
}

type invAgent struct {
	method string
	params map[string]any
	out    string
	err    error
}

func (a *invAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.method = method
	a.params, _ = params.(map[string]any)
	return json.RawMessage(a.out), a.err
}

func TestReadHostDisks(t *testing.T) {
	ag := &invAgent{out: `{"mount":"/","entries":[{"username":"shop","used_kb":2048,"limit_kb":10240}],"partial":false}`}
	got, err := ReadHostDisks(context.Background(), ag, "/")
	if err != nil {
		t.Fatal(err)
	}
	if ag.method != "system.quota_inventory" || ag.params["mount"] != "/" {
		t.Errorf("called %s %v", ag.method, ag.params)
	}
	if got["shop"] != (HostDisk{UsedKB: 2048, LimitKB: 10240}) {
		t.Errorf("shop = %+v", got["shop"])
	}

	ag.out = `{"entries":[{"username":"shop","used_kb":1,"limit_kb":2}],"partial":true}`
	got, err = ReadHostDisks(context.Background(), ag, "/")
	if !errors.Is(err, ErrHostDiskPartial) || got["shop"].UsedKB != 1 {
		t.Errorf("partial: %v %+v", err, got)
	}

	ag.err = errors.New("agent down")
	if _, err := ReadHostDisks(context.Background(), ag, "/"); err == nil {
		t.Error("agent error swallowed")
	}
	ag.err, ag.out = nil, `not json`
	if _, err := ReadHostDisks(context.Background(), ag, "/"); err == nil {
		t.Error("bad answer accepted")
	}
	if _, err := ReadHostDisks(context.Background(), &invAgent{}, ""); err == nil {
		t.Error("no mount accepted")
	}
}

func TestHostDiskOf(t *testing.T) {
	disks := map[string]HostDisk{"shop": {UsedKB: 5}}
	if d, ok := HostDiskOf(disks, nil, "shop"); !ok || d.UsedKB != 5 {
		t.Errorf("listed user: %+v %t", d, ok)
	}
	if d, ok := HostDiskOf(disks, nil, "blog"); !ok || d != (HostDisk{}) {
		t.Errorf("complete listing without the user: %+v %t, want nothing used, found", d, ok)
	}
	if _, ok := HostDiskOf(disks, ErrHostDiskPartial, "shop"); !ok {
		t.Error("partial listing with the user: not found")
	}
	if _, ok := HostDiskOf(disks, ErrHostDiskPartial, "blog"); ok {
		t.Error("partial listing without the user: found")
	}
	if _, ok := HostDiskOf(nil, errors.New("agent down"), "shop"); ok {
		t.Error("failed listing: found")
	}
}
