package limitsync

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type users map[string]*models.User

func (u users) FindByID(_ context.Context, id string) (*models.User, error) {
	if x, ok := u[id]; ok {
		return x, nil
	}
	return nil, repository.ErrNotFound
}

type packages struct {
	pkg *models.HostingPackage
	err error
}

func (p packages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return p.pkg, p.err
}

type overrides struct {
	o   *models.UserLimitOverride
	err error
}

func (o overrides) FindByUserID(context.Context, string) (*models.UserLimitOverride, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.o == nil {
		return nil, repository.ErrNotFound
	}
	return o.o, nil
}

type settings struct {
	s   *models.ServerSettings
	err error
}

func (s settings) Get(context.Context) (*models.ServerSettings, error) { return s.s, s.err }

func strp(s string) *string { return &s }
func u32(v uint32) *uint32  { return &v }

func tenant() Tenant {
	return Tenant{
		Users:     users{"u1": {ID: "u1", Username: strp("shop"), PackageID: strp("p1")}, "u2": {ID: "u2", Username: strp("bare")}},
		Packages:  packages{pkg: &models.HostingPackage{ID: "p1", DiskQuotaMB: 1000, CPUQuotaPercent: 50}},
		Overrides: overrides{},
		Settings:  settings{s: &models.ServerSettings{DiskQuotaEnabled: true}},
	}
}

func TestPlanFor_PackageWithShares(t *testing.T) {
	plan, err := tenant().PlanFor(context.Background(), "u1", "/", limits.IsolatedShares{SharesMB: 300})
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if plan.Kind != limits.PlanApply || plan.QuotaMount != "/" || plan.TenantDiskQuotaMB() != 700 || plan.Effective.CPUQuotaPercent != 50 {
		t.Fatalf("plan = %+v (tenant disk %d), want Apply on / with 700 MB and cpu 50", plan, plan.TenantDiskQuotaMB())
	}
}

func TestPlanFor_OverrideWins(t *testing.T) {
	tn := tenant()
	tn.Overrides = overrides{o: &models.UserLimitOverride{UserID: "u1", DiskQuotaMB: u32(2000)}}
	plan, err := tn.PlanFor(context.Background(), "u1", "/", limits.IsolatedShares{SharesMB: 300})
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if plan.TenantDiskQuotaMB() != 1700 {
		t.Fatalf("tenant disk = %d, want 1700 (override 2000 less 300)", plan.TenantDiskQuotaMB())
	}
}

func TestPlanFor_NoPackageNoOverrideClears(t *testing.T) {
	plan, err := tenant().PlanFor(context.Background(), "u2", "/", limits.IsolatedShares{})
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if plan.Kind != limits.PlanClear {
		t.Fatalf("plan = %+v, want Clear", plan)
	}
}

func TestPlanFor_QuotaOffBlanksTheMount(t *testing.T) {
	tn := tenant()
	tn.Settings = settings{s: &models.ServerSettings{DiskQuotaEnabled: false}}
	plan, err := tn.PlanFor(context.Background(), "u1", "/", limits.IsolatedShares{})
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if plan.Kind != limits.PlanApply || plan.QuotaMount != "" {
		t.Fatalf("plan = %+v, want Apply with no mount", plan)
	}
}

// Every lookup failure is an error, never a guess.
func TestPlanFor_LookupFailuresAreErrors(t *testing.T) {
	down := errors.New("db down")
	cases := map[string]func(*Tenant){
		"user":     func(tn *Tenant) { tn.Users = users{} },
		"package":  func(tn *Tenant) { tn.Packages = packages{err: down} },
		"override": func(tn *Tenant) { tn.Overrides = overrides{err: down} },
		"settings": func(tn *Tenant) { tn.Settings = settings{err: down} },
		"unwired":  func(tn *Tenant) { tn.Overrides = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tn := tenant()
			mutate(&tn)
			if _, err := tn.PlanFor(context.Background(), "u1", "/", limits.IsolatedShares{}); err == nil {
				t.Fatal("PlanFor succeeded, want an error")
			}
		})
	}
}
