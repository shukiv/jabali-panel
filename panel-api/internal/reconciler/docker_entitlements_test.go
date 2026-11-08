package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type entPackages struct {
	repository.PackageRepository
	pkg *models.HostingPackage
	err error
}

func (f *entPackages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, f.err
}

// entApp is a running tenant app owned by u1; age orders them (higher = older).
func entApp(slug string, age int, dataBytes int64) *models.DockerApp {
	uid := "u1"
	return &models.DockerApp{
		ID: "app-" + slug, UserID: &uid, Slug: slug, Status: models.DockerAppStatusRunning,
		CreatedAt: time.Unix(1_700_000_000, 0).Add(-time.Duration(age) * time.Hour), DataBytes: dataBytes,
	}
}

func entReconciler(users *gateUserRepo, pkgs *entPackages) (*Reconciler, *gateAgent) {
	ag := &gateAgent{}
	return &Reconciler{agent: ag, dockerApps: &gateDockerRepo{}, users: users, packages: pkgs, log: slog.Default()}, ag
}

func ownerOn(pkgID *string) *gateUserRepo {
	name := "alice"
	return &gateUserRepo{user: &models.User{ID: "u1", Username: &name, PackageID: pkgID}}
}

func stoppedSlugs(ag *gateAgent) []string {
	var out []string
	for _, p := range ag.calls["docker_app.stop"] {
		out = append(out, p["slug"].(string))
	}
	sort.Strings(out)
	return out
}

func equalSlugs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDockerEntitlements_UnreadablePackageOrOwnerSkipsTheTenant(t *testing.T) {
	pkgID := "p1"
	for name, c := range map[string]struct {
		users *gateUserRepo
		pkgs  *entPackages
	}{
		"package unreadable":    {ownerOn(&pkgID), &entPackages{err: errors.New("hosting_packages: connection refused")}},
		"package loaded as nil": {ownerOn(&pkgID), &entPackages{}},
		"owner unreadable":      {&gateUserRepo{err: errors.New("users: connection refused")}, &entPackages{pkg: &models.HostingPackage{ID: pkgID}}},
		"owner loaded as nil":   {&gateUserRepo{}, &entPackages{pkg: &models.HostingPackage{ID: pkgID}}},
	} {
		r, ag := entReconciler(c.users, c.pkgs)
		r.enforceDockerEntitlements(context.Background(), []*models.DockerApp{entApp("ghost", 2, 0), entApp("memos", 1, 0)})
		if got := stoppedSlugs(ag); len(got) != 0 {
			t.Fatalf("%s: a read failure must not stop the tenant's apps, stopped %v", name, got)
		}
	}
}

func TestDockerEntitlements_NoPackageStopsTheTenantsApps(t *testing.T) {
	gone := "deleted-package"
	for name, c := range map[string]struct {
		users *gateUserRepo
		pkgs  *entPackages
	}{
		"no package":      {ownerOn(nil), &entPackages{}},
		"deleted package": {ownerOn(&gone), &entPackages{err: repository.ErrNotFound}},
		"deleted owner":   {&gateUserRepo{err: repository.ErrNotFound}, &entPackages{}},
	} {
		r, ag := entReconciler(c.users, c.pkgs)
		r.enforceDockerEntitlements(context.Background(), []*models.DockerApp{entApp("ghost", 2, 0), entApp("memos", 1, 0)})
		if got := stoppedSlugs(ag); !equalSlugs(got, []string{"ghost", "memos"}) {
			t.Fatalf("%s: tenant Docker needs a package, want both stopped, got %v", name, got)
		}
	}
}

func TestDockerEntitlements_AllowlistAppliesWithinTheDiskQuota(t *testing.T) {
	pkgID := "p1"
	for name, diskMB := range map[string]uint32{"no disk quota": 0, "under the disk quota": 100} {
		pkgs := &entPackages{pkg: &models.HostingPackage{ID: pkgID, MaxDockerApps: 5, DiskQuotaMB: diskMB, DockerAppSlugs: "ghost"}}
		r, ag := entReconciler(ownerOn(&pkgID), pkgs)
		r.enforceDockerEntitlements(context.Background(), []*models.DockerApp{entApp("ghost", 2, 1<<20), entApp("memos", 1, 1<<20)})
		if got := stoppedSlugs(ag); !equalSlugs(got, []string{"memos"}) {
			t.Fatalf("%s: want only the app off the allowlist stopped, got %v", name, got)
		}
	}
}

func TestDockerEntitlements_EachAppIsStoppedOnce(t *testing.T) {
	pkgID := "p1"
	// One app allowed, a 1 MB disk quota with 3 MB used, and only ghost on the
	// allowlist: memos and wiki are over the count, everything is over the
	// disk quota, memos and wiki are off the allowlist.
	pkgs := &entPackages{pkg: &models.HostingPackage{ID: pkgID, MaxDockerApps: 1, DiskQuotaMB: 1, DockerAppSlugs: "ghost"}}
	r, ag := entReconciler(ownerOn(&pkgID), pkgs)
	r.enforceDockerEntitlements(context.Background(), []*models.DockerApp{
		entApp("ghost", 3, 1<<20), entApp("wiki", 2, 1<<20), entApp("memos", 1, 1<<20),
	})
	if got := stoppedSlugs(ag); !equalSlugs(got, []string{"ghost", "memos", "wiki"}) {
		t.Fatalf("want each app stopped exactly once, got %v", got)
	}
}

func TestDockerEntitlements_WithinEveryLimitNothingStops(t *testing.T) {
	pkgID := "p1"
	pkgs := &entPackages{pkg: &models.HostingPackage{ID: pkgID, MaxDockerApps: 2, DiskQuotaMB: 100, DockerAppSlugs: "ghost,memos"}}
	r, ag := entReconciler(ownerOn(&pkgID), pkgs)
	r.enforceDockerEntitlements(context.Background(), []*models.DockerApp{entApp("ghost", 2, 1<<20), entApp("memos", 1, 1<<20)})
	if got := stoppedSlugs(ag); len(got) != 0 {
		t.Fatalf("nothing is over a limit, stopped %v", got)
	}
}
