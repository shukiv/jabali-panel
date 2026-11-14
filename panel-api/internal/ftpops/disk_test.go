package ftpops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/limitsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeLimits plans a tenant with a package disk quota of quotaMB (0: none).
type fakeLimits struct {
	quotaMB uint32
	err     error
}

func (f *fakeLimits) PlanFor(_ context.Context, _, quotaMount string, shares limits.IsolatedShares) (limits.LimitsPlan, error) {
	if f.err != nil {
		return limits.LimitsPlan{}, f.err
	}
	return limits.PlanFor(&limits.PackageLimits{DiskQuotaMB: f.quotaMB}, nil, true, quotaMount, shares), nil
}

const (
	inventoryOp = "agent:system.quota_inventory[mount]"
	applyOp     = "agent:user.limits.apply[cpu_quota_percent,disk_quota_mb,io_read_mbps,io_write_mbps,max_tasks,memory_limit_mb,quota_mount,username]"
	isoCreateOp = "agent:ftpaccount.create[ftp_access,home_path,isolated,jail_path,password,quota_mb,quota_mount,tenant_username,uid,username,webdav_access]"
)

// aliceUses is a quota listing where alice uses usedMB under a limitMB limit.
func aliceUses(usedMB, limitMB uint64) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"mount":   "/",
		"partial": false,
		"entries": []map[string]any{{"username": "alice", "used_kb": usedMB * 1024, "limit_kb": limitMB * 1024}},
	})
	return raw
}

// isolatedDeps: alice's package has a 1000 MB disk quota, and the host
// answers the quota listings in order.
func isolatedDeps(log *transcript, readings ...json.RawMessage) (Deps, *fakeAccounts, *fakeAgent) {
	d, accts, ag := newDeps(log)
	d.QuotaMount = "/"
	d.Limits = &fakeLimits{quotaMB: 1000}
	if len(readings) > 0 {
		ag.answers["system.quota_inventory"] = readings
	}
	return d, accts, ag
}

func isolatedReq(quotaMB uint32) CreateRequest {
	r := createReq()
	r.Isolated, r.QuotaMB = true, quotaMB
	return r
}

func wantOrder(t *testing.T, log transcript, ops ...string) {
	t.Helper()
	at := -1
	for _, op := range ops {
		i := -1
		for j := at + 1; j < len(log); j++ {
			if log[j] == op {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("transcript = %v, want %v in this order", log, ops)
		}
		at = i
	}
}

func wantReason(t *testing.T, err, reason error) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, reason) {
		t.Fatalf("err = %v, want ValidationError(%v)", err, reason)
	}
}

// The tenant's own limit drops by the new account's quota before the account
// exists on the host.
func TestCreate_IsolatedSetsItsQuotaAsideFromTheTenant(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log, aliceUses(100, 1000))

	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantOrder(t, log, "repo.Reserve", inventoryOp, applyOp, inventoryOp, isoCreateOp)
	if !reflect.DeepEqual(ag.applied, []uint32{700}) {
		t.Errorf("tenant disk limits applied = %v, want [700]", ag.applied)
	}
}

// Other isolated accounts already hold part of the quota.
func TestCreate_IsolatedCountsTheOtherAccounts(t *testing.T) {
	var log transcript
	d, accts, ag := isolatedDeps(&log, aliceUses(100, 800))
	accts.isoShares = 200

	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !reflect.DeepEqual(ag.applied, []uint32{500}) {
		t.Errorf("tenant disk limits applied = %v, want [500]", ag.applied)
	}
}

// A tenant whose files already use the room is refused, and nothing changes.
func TestCreate_IsolatedRefusedWhenTheTenantUsesTheRoom(t *testing.T) {
	var log transcript
	d, accts, ag := isolatedDeps(&log, aliceUses(701, 1000))

	_, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300))
	wantReason(t, err, ErrNoDiskRoom)
	if want := "your files use 701 MB, more than the 700 MB of your disk quota this account's quota would leave you; free some space or pick a smaller quota"; err.Error() != want {
		t.Errorf("detail = %q, want %q", err.Error(), want)
	}
	if log.has(applyOp) || log.has(isoCreateOp) || !log.has("repo.Delete") || len(accts.reserved) != 0 {
		t.Fatalf("a refused create changes no limit, creates nothing and releases its row; transcript = %v", log)
	}
	if len(ag.applied) != 0 {
		t.Errorf("limits applied: %v", ag.applied)
	}
}

// Exactly at the new limit still fits.
func TestCreate_IsolatedFitsExactly(t *testing.T) {
	var log transcript
	d, _, _ := isolatedDeps(&log, aliceUses(700, 1000))
	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestCreate_IsolatedRefusedWithoutAHostReading(t *testing.T) {
	cases := map[string]func(*Deps, *fakeAccounts, *fakeAgent){
		"listing fails": func(_ *Deps, _ *fakeAccounts, ag *fakeAgent) {
			ag.errOn["system.quota_inventory"] = errors.New("repquota failed")
		},
		"tenant missing from a partial listing": func(_ *Deps, _ *fakeAccounts, ag *fakeAgent) {
			ag.answers["system.quota_inventory"] = []json.RawMessage{json.RawMessage(`{"mount":"/","entries":[],"partial":true}`)}
		},
		"limits unreadable": func(d *Deps, _ *fakeAccounts, _ *fakeAgent) {
			d.Limits = &fakeLimits{err: errors.New("db down")}
		},
		"shares unreadable": func(_ *Deps, accts *fakeAccounts, _ *fakeAgent) {
			accts.sumErr = errors.New("db down")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var log transcript
			d, accts, ag := isolatedDeps(&log)
			mutate(&d, accts, ag)

			_, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300))
			wantReason(t, err, ErrDiskRoomUnknown)
			if log.has(applyOp) || log.has(isoCreateOp) || !log.has("repo.Delete") {
				t.Fatalf("transcript = %v, want no limit change, no create, the row released", log)
			}
		})
	}
}

// A complete listing without the tenant: it uses nothing on the mount.
func TestCreate_IsolatedTenantAbsentFromACompleteListing(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log, json.RawMessage(`{"mount":"/","entries":[],"partial":false}`))
	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !reflect.DeepEqual(ag.applied, []uint32{700}) {
		t.Errorf("tenant disk limits applied = %v, want [700]", ag.applied)
	}
}

// The host create fails after the limit dropped: the row goes first, then the
// tenant's limit comes back.
func TestCreate_IsolatedHostFailureGivesTheRoomBack(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log, aliceUses(100, 1000))
	ag.errOn["ftpaccount.create"] = errors.New("useradd failed")

	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err == nil {
		t.Fatal("Create succeeded, want the host error")
	}
	wantOrder(t, log, applyOp, isoCreateOp, "repo.Delete", applyOp)
	if !reflect.DeepEqual(ag.applied, []uint32{700, 1000}) {
		t.Errorf("tenant disk limits applied = %v, want [700 1000]", ag.applied)
	}
}

// The tenant wrote into the room between the reading and the new limit.
func TestCreate_IsolatedTenantFilledTheRoomMeanwhile(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log, aliceUses(100, 1000), aliceUses(750, 700))

	_, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300))
	wantReason(t, err, ErrNoDiskRoom)
	if log.has(isoCreateOp) {
		t.Fatalf("created anyway; transcript = %v", log)
	}
	wantOrder(t, log, applyOp, inventoryOp, "repo.Delete", applyOp)
	if !reflect.DeepEqual(ag.applied, []uint32{700, 1000}) {
		t.Errorf("tenant disk limits applied = %v, want [700 1000]", ag.applied)
	}
}

func TestCreate_IsolatedLimitApplyFailureRestores(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log, aliceUses(100, 1000))
	applyErr := errors.New("setquota failed")
	ag.errOn["user.limits.apply"] = applyErr

	_, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300))
	if !errors.Is(err, applyErr) {
		t.Fatalf("err = %v, want the apply error", err)
	}
	if log.has(isoCreateOp) {
		t.Fatalf("created anyway; transcript = %v", log)
	}
	wantOrder(t, log, applyOp, "repo.Delete", applyOp)
}

// Without a tenant disk quota there is nothing to set aside or read.
func TestCreate_IsolatedNoTenantQuotaNoReading(t *testing.T) {
	var log transcript
	d, _, _ := isolatedDeps(&log)
	d.Limits = &fakeLimits{} // no disk quota

	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if log.has(inventoryOp) || log.has(applyOp) {
		t.Fatalf("transcript = %v, want no reading and no limit change", log)
	}
}

func isolatedAccount() *models.FtpAccount {
	a := testAccount()
	a.Isolated, a.QuotaMB = true, 300
	return a
}

// Deleting the last isolated account gives the tenant its whole quota back.
func TestDelete_IsolatedGivesTheRoomBack(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log)

	if err := Delete(context.Background(), d, isolatedAccount(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wantOrder(t, log, deleteOp, "repo.Delete", applyOp)
	if log.has(inventoryOp) {
		t.Errorf("read the host with no isolated account left; transcript = %v", log)
	}
	if !reflect.DeepEqual(ag.applied, []uint32{1000}) {
		t.Errorf("tenant disk limits applied = %v, want [1000]", ag.applied)
	}
}

// With other isolated accounts left the tenant gets its share, and one over
// its share keeps what it uses.
func TestDelete_IsolatedWithOthersLeft(t *testing.T) {
	cases := []struct {
		name    string
		reading json.RawMessage
		want    uint32
	}{
		{"under its share", aliceUses(100, 500), 800},
		{"over its share", aliceUses(900, 964), 900 + limits.GrandfatherHeadroomMB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log transcript
			d, accts, ag := isolatedDeps(&log, tc.reading)
			accts.isoShares = 200

			if err := Delete(context.Background(), d, isolatedAccount(), "alice"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if !reflect.DeepEqual(ag.applied, []uint32{tc.want}) {
				t.Errorf("tenant disk limits applied = %v, want [%d]", ag.applied, tc.want)
			}
		})
	}
}

// No host reading: the tenant's limit is left for the reconciler, the delete
// still succeeds.
func TestDelete_IsolatedNoReadingLeavesTheLimit(t *testing.T) {
	var log transcript
	d, accts, ag := isolatedDeps(&log)
	accts.isoShares = 200
	ag.errOn["system.quota_inventory"] = errors.New("repquota failed")

	if err := Delete(context.Background(), d, isolatedAccount(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if log.has(applyOp) {
		t.Fatalf("applied without a reading; transcript = %v", log)
	}
}

func TestDelete_NotIsolatedLeavesTheLimit(t *testing.T) {
	var log transcript
	d, _, _ := isolatedDeps(&log)

	if err := Delete(context.Background(), d, testAccount(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if log.has(applyOp) || log.has(inventoryOp) {
		t.Fatalf("transcript = %v, want no limit change", log)
	}
}

// The reservation checks the package's quota; a lower limit override is the
// tenant's quota, and the shares can't exceed it either.
func TestCreate_IsolatedSharesOverTheEffectiveQuotaRefused(t *testing.T) {
	var log transcript
	d, accts, _ := isolatedDeps(&log, aliceUses(0, 0))
	accts.isoShares = 900 // with this account's 300: 1200 of a 1000 MB quota

	_, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300))
	if !errors.Is(err, repository.ErrFtpQuotaSplitExceeded) {
		t.Fatalf("err = %v, want ErrFtpQuotaSplitExceeded", err)
	}
	if log.has(applyOp) || log.has(isoCreateOp) || !log.has("repo.Delete") {
		t.Fatalf("transcript = %v, want no limit change, no create, the row released", log)
	}
}

// heldDuring records whether alice's limits lock was held at each of the
// given host calls.
func heldDuring(ag *fakeAgent, commands ...string) *[]string {
	var free []string
	ag.afterCall = func(command string) {
		for _, c := range commands {
			if c != command {
				continue
			}
			if unlock, ok := limitsync.TryLockTenant(testOwner.UserID); ok {
				unlock()
				free = append(free, command)
			}
		}
	}
	return &free
}

// The limits reconciler stays off the tenant while its share and limit
// change.
func TestCreate_IsolatedHoldsTheTenantLock(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log, aliceUses(100, 1000))
	free := heldDuring(ag, "user.limits.apply", "ftpaccount.create")

	if _, err := Create(context.Background(), d, testOwner, testPkg, isolatedReq(300)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(*free) != 0 {
		t.Fatalf("tenant lock not held at %v", *free)
	}
	if unlock, ok := limitsync.TryLockTenant(testOwner.UserID); !ok {
		t.Fatal("tenant lock still held after Create")
	} else {
		unlock()
	}
}

func TestDelete_IsolatedHoldsTheTenantLock(t *testing.T) {
	var log transcript
	d, _, ag := isolatedDeps(&log)
	free := heldDuring(ag, "user.limits.apply")

	if err := Delete(context.Background(), d, isolatedAccount(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !log.has(applyOp) || len(*free) != 0 {
		t.Fatalf("tenant lock not held at %v; transcript = %v", *free, log)
	}
}
