package reconciler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type bwqDomains struct {
	repository.DomainRepository
	rows map[string]*models.Domain
}

func (f *bwqDomains) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	out := []models.Domain{}
	for _, d := range f.rows {
		if d.UserID == userID {
			out = append(out, *d)
		}
	}
	return out, int64(len(out)), nil
}

func (f *bwqDomains) Update(_ context.Context, d *models.Domain) error {
	cp := *d
	f.rows[d.ID] = &cp
	return nil
}

type bwqDaily struct {
	repository.BWDailyRepository
	bytes uint64
}

func (f *bwqDaily) SumByDomainForUser(context.Context, string, time.Time, time.Time) (map[string]uint64, error) {
	return map[string]uint64{"site": f.bytes}, nil
}

// bwqReconciler builds the sweep harness: one tenant (u1, package quota
// 100 MiB, auto-suspend on) owning an enabled domain "site" and a domain
// "parked" that was disabled by hand.
func bwqReconciler(bw *bwqDaily) (*Reconciler, *bwqDomains) {
	pid := "p1"
	doms := &bwqDomains{rows: map[string]*models.Domain{
		"site":   {ID: "site", UserID: "u1", Name: "site.example", IsEnabled: true},
		"parked": {ID: "parked", UserID: "u1", Name: "parked.example", IsEnabled: false},
	}}
	r := New(doms, &dbqUsersRepo{rows: []models.User{{ID: "u1", PackageID: &pid}}}, &fakeAgent{}, slog.Default(), Config{})
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{BandwidthQuotaEnforceEnabled: true}}
	r.WithPackages(&dbqPkgRepo{pkg: &models.HostingPackage{ID: "p1", BandwidthQuotaMB: 100}})
	r.WithBandwidthQuotaEnforce(bw, nil)
	return r, doms
}

// bwqSweep runs the loop now, past its hourly gate.
func (r *Reconciler) bwqSweep(ctx context.Context) {
	r.bwEnforceLastRun = time.Time{}
	r.reconcileBandwidthQuotaEnforce(ctx)
}

// Over quota, every enabled domain is suspended; under 80% they come back.
// A domain the admin had already disabled is not taken over by the loop, so
// the restore does not switch it on.
func TestBandwidthQuota_SuspendAndRestoreLeaveManualDisablesAlone(t *testing.T) {
	ctx := context.Background()
	bw := &bwqDaily{bytes: 150 << 20}
	r, doms := bwqReconciler(bw)

	r.bwqSweep(ctx)
	if s := doms.rows["site"]; s.IsEnabled || !s.IsQuotaSuspended {
		t.Fatalf("over quota: site enabled=%v quota_suspended=%v, want false/true", s.IsEnabled, s.IsQuotaSuspended)
	}
	if p := doms.rows["parked"]; p.IsEnabled || p.IsQuotaSuspended {
		t.Fatalf("over quota: parked enabled=%v quota_suspended=%v, want false/false (manual disable)", p.IsEnabled, p.IsQuotaSuspended)
	}

	bw.bytes = 10 << 20
	r.bwqSweep(ctx)
	if s := doms.rows["site"]; !s.IsEnabled || s.IsQuotaSuspended {
		t.Fatalf("under 80%%: site enabled=%v quota_suspended=%v, want true/false", s.IsEnabled, s.IsQuotaSuspended)
	}
	if p := doms.rows["parked"]; p.IsEnabled {
		t.Fatal("under 80%: the manually disabled domain was switched on")
	}
}

// Between 80% and 100% nothing changes in either direction.
func TestBandwidthQuota_BetweenWarnAndLimitHolds(t *testing.T) {
	ctx := context.Background()
	bw := &bwqDaily{bytes: 150 << 20}
	r, doms := bwqReconciler(bw)
	r.bwqSweep(ctx)

	bw.bytes = 90 << 20
	r.bwqSweep(ctx)
	if s := doms.rows["site"]; s.IsEnabled || !s.IsQuotaSuspended {
		t.Fatalf("at 90%%: site enabled=%v quota_suspended=%v, want it still suspended", s.IsEnabled, s.IsQuotaSuspended)
	}
}
