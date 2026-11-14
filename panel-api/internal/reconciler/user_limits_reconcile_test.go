package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/limitsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type limitsUsers struct {
	repository.UserRepository
	users []models.User
}

func (f *limitsUsers) List(context.Context, repository.ListOptions) ([]models.User, int64, error) {
	return f.users, int64(len(f.users)), nil
}

type limitsPkgs struct {
	repository.PackageRepository
}

func (limitsPkgs) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	return &models.HostingPackage{ID: id, DiskQuotaMB: 1000}, nil
}

type limitsOverrides struct {
	repository.UserLimitOverrideRepository
}

func (limitsOverrides) ListAll(context.Context) ([]models.UserLimitOverride, error) { return nil, nil }

type limitsFtp struct {
	repository.FtpAccountRepository
	shares map[string]uint64
	err    error
	// now overrides a tenant's share when it is read again before its apply
	// (else the share above); unlocked lists tenants read again outside
	// their limitsync lock.
	now      map[string]uint64
	unlocked []string
}

func (f *limitsFtp) SumIsolatedQuotaByUser(context.Context) (map[string]uint64, error) {
	return f.shares, f.err
}

func (f *limitsFtp) SumIsolatedQuotaByUserID(_ context.Context, userID string) (uint64, error) {
	if unlock, ok := limitsync.TryLockTenant(userID); ok {
		unlock()
		f.unlocked = append(f.unlocked, userID)
	}
	if mb, ok := f.now[userID]; ok {
		return mb, nil
	}
	return f.shares[userID], nil
}

func limitsUser(id string) models.User {
	name, pkg := id, "pkg1"
	return models.User{ID: id, Username: &name, PackageID: &pkg}
}

// runLimitsPass runs one limits pass and returns the disk limit sent per
// username, and whether the host quota listing was read.
func runLimitsPass(t *testing.T, ag *fakeAgent, ftp *limitsFtp, mount string) (map[string]uint32, bool) {
	t.Helper()
	users := &limitsUsers{users: []models.User{limitsUser("shop"), limitsUser("blog"), limitsUser("big"), limitsUser("gone")}}
	r := New(nil, users, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{}).
		WithPackages(limitsPkgs{}).
		WithLimitOverrides(limitsOverrides{}).
		WithQuotaMount(mount).
		WithFtpAccounts(ftp)
	r.ReconcileUserLimits(context.Background())

	sent := map[string]uint32{}
	listed := false
	for _, c := range ag.calls {
		switch c.method {
		case "user.limits.apply":
			p := c.params.(map[string]any)
			sent[p["username"].(string)] = p["disk_quota_mb"].(uint32)
		case "system.quota_inventory":
			listed = true
		}
	}
	return sent, listed
}

func inventory(t *testing.T, partial bool, rows map[string][2]uint64) json.RawMessage {
	t.Helper()
	var entries []map[string]any
	for name, r := range rows {
		entries = append(entries, map[string]any{"username": name, "used_kb": r[0], "limit_kb": r[1]})
	}
	raw, err := json.Marshal(map[string]any{"mount": "/", "entries": entries, "partial": partial})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReconcileUserLimits_TenantsKeepRoomForIsolatedAccounts(t *testing.T) {
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"system.quota_inventory": inventory(t, true, map[string][2]uint64{
			"shop": {100 * 1024, 1000 * 1024}, // under its share
			"big":  {900 * 1024, 1000 * 1024}, // over its share already
		}),
	}}
	ftp := &limitsFtp{shares: map[string]uint64{"shop": 300, "big": 300, "gone": 300}}

	sent, listed := runLimitsPass(t, ag, ftp, "/")

	if !listed {
		t.Error("the host quota listing wasn't read")
	}
	want := map[string]uint32{
		"shop": 700,                                // 1000 less 300
		"blog": 1000,                               // no isolated accounts
		"big":  900 + limits.GrandfatherHeadroomMB, // keeps what it uses
	}
	for name, mb := range want {
		if sent[name] != mb {
			t.Errorf("%s: disk %d MB, want %d", name, sent[name], mb)
		}
	}
	// Missing from a partial listing: its disk limit is unknown, so left as
	// it is; the rest of its limits still apply.
	if m := mountsSent(ag); m["gone"] != "" || m["shop"] != "/" {
		t.Errorf("quota mounts sent = %v, want none for gone and / for shop", m)
	}
}

// mountsSent is the quota_mount of each user.limits.apply call, by username.
func mountsSent(ag *fakeAgent) map[string]string {
	out := map[string]string{}
	for _, c := range ag.calls {
		if c.method == "user.limits.apply" {
			p := c.params.(map[string]any)
			out[p["username"].(string)] = p["quota_mount"].(string)
		}
	}
	return out
}

func TestReconcileUserLimits_NoHostReadingLeavesTenantsWithSharesAlone(t *testing.T) {
	ag := &fakeAgent{errByMethod: map[string]error{"system.quota_inventory": errors.New("repquota failed")}}
	ftp := &limitsFtp{shares: map[string]uint64{"shop": 300}}

	sent, _ := runLimitsPass(t, ag, ftp, "/")

	// shop's cgroup limits still apply, its disk limit doesn't: no mount.
	if m := mountsSent(ag); m["shop"] != "" || m["blog"] != "/" {
		t.Errorf("quota mounts sent = %v, want none for shop and / for blog", m)
	}
	if _, ok := sent["shop"]; !ok {
		t.Error("shop: its other limits weren't applied")
	}
	if sent["blog"] != 1000 {
		t.Errorf("blog (no isolated accounts): %d MB, want 1000", sent["blog"])
	}
}

func TestReconcileUserLimits_NoSharesNoPass(t *testing.T) {
	ag := &fakeAgent{}
	ftp := &limitsFtp{err: errors.New("db down")}

	sent, _ := runLimitsPass(t, ag, ftp, "/")

	if len(sent) != 0 {
		t.Errorf("applied %v without knowing the isolated accounts' quotas", sent)
	}
}

func TestReconcileUserLimits_WithoutAQuotaMountTheShareStillApplies(t *testing.T) {
	ag := &fakeAgent{}
	ftp := &limitsFtp{shares: map[string]uint64{"shop": 300}}

	sent, listed := runLimitsPass(t, ag, ftp, "")

	if listed {
		t.Error("read the host quota listing without a quota mount")
	}
	if sent["shop"] != 700 || sent["blog"] != 1000 {
		t.Errorf("sent %v, want shop 700 and blog 1000", sent)
	}
}

// An isolated account created or deleted after the pass read the shares set
// the tenant's limit itself; the pass's plan would put the old one back.
func TestReconcileUserLimits_ShareChangedDuringThePassIsLeft(t *testing.T) {
	ag := &fakeAgent{}
	ftp := &limitsFtp{now: map[string]uint64{"shop": 300}}

	sent, _ := runLimitsPass(t, ag, ftp, "")

	if _, ok := sent["shop"]; ok {
		t.Errorf("shop: applied %d MB from a share that changed", sent["shop"])
	}
	if sent["blog"] != 1000 {
		t.Errorf("blog: %d MB, want 1000", sent["blog"])
	}
	if len(ftp.unlocked) != 0 {
		t.Errorf("shares read again outside the tenant lock: %v", ftp.unlocked)
	}
}

// A tenant whose isolated account change is under way is left alone.
func TestReconcileUserLimits_TenantUnderChangeIsLeft(t *testing.T) {
	unlock := limitsync.LockTenant("shop")
	defer unlock()
	ag := &fakeAgent{}

	sent, _ := runLimitsPass(t, ag, &limitsFtp{}, "")

	if _, ok := sent["shop"]; ok {
		t.Errorf("shop: applied %d MB while its FTP account change was under way", sent["shop"])
	}
	if sent["blog"] != 1000 {
		t.Errorf("blog: %d MB, want 1000", sent["blog"])
	}
}

func TestReconcileUserLimits_NoTenantWithSharesNoListing(t *testing.T) {
	ag := &fakeAgent{}
	sent, listed := runLimitsPass(t, ag, &limitsFtp{}, "/")
	if listed {
		t.Error("read the host quota listing with no isolated accounts")
	}
	if sent["shop"] != 1000 {
		t.Errorf("shop: %d MB, want 1000", sent["shop"])
	}
}
