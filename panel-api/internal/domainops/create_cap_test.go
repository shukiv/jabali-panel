package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// racingStore lands another domain for the same owner between the early quota
// check and the insert: two creates racing for the last free slot.
type racingStore struct{ *createStore }

func (r racingStore) CreateWithinCap(ctx context.Context, d *models.Domain, maxDomains uint32) error {
	r.byName["racer.example.com"] = &models.Domain{ID: "racer", UserID: d.UserID, Name: "racer.example.com"}
	return r.createStore.CreateWithinCap(ctx, d, maxDomains)
}

func TestCreate_DomainCapHeldAtTheInsert(t *testing.T) {
	ctx := context.Background()
	uname := "alice"
	pkgID := "p1"
	withPkg := &models.User{ID: "u1", Username: &uname, PackageID: &pkgID}
	packages := func(max uint32) *fakePackages {
		return &fakePackages{rows: map[string]*models.HostingPackage{pkgID: {ID: pkgID, MaxDomains: max}}}
	}
	deps := func(s CreateDomainStore, owner *models.User, p PackageFinder) CreateDeps {
		return CreateDeps{Domains: s, Users: createOwners{"u1": owner}, Packages: p, Ownership: ownershipPolicy{require: false}}
	}
	in := CreateInput{OwnerID: "u1", Name: "shop.example.com"}
	existing := &models.Domain{ID: "old", UserID: "u1", Name: "old.example.com"}

	t.Run("the package limit goes to the locked insert", func(t *testing.T) {
		s := newCreateStore(existing)
		if _, err := Create(ctx, deps(s, withPkg, packages(2)), CreateHooks{}, in); err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(s.caps) != 1 || s.caps[0] != 2 {
			t.Fatalf("want one capped insert with max 2, caps=%v", s.caps)
		}
	})

	t.Run("a create that loses the race for the last slot is refused", func(t *testing.T) {
		s := newCreateStore(existing)
		_, err := Create(ctx, deps(racingStore{s}, withPkg, packages(2)), CreateHooks{}, in)
		var qe *DomainQuotaError
		if !errors.As(err, &qe) || !errors.Is(err, ErrDomainQuotaExceeded) || qe.Max != 2 {
			t.Fatalf("want *DomainQuotaError{Max: 2}, got %v", err)
		}
		if _, ok := s.byName["shop.example.com"]; ok {
			t.Fatal("the losing create was inserted")
		}
	})

	t.Run("a package that cannot be read for the insert's limit refuses the create", func(t *testing.T) {
		s := newCreateStore(existing)
		p := packages(2)
		p.failFrom = 2 // the early check reads it, the insert's limit cannot
		_, err := Create(ctx, deps(s, withPkg, p), CreateHooks{}, in)
		if !errors.Is(err, ErrQuotaUnavailable) {
			t.Fatalf("want ErrQuotaUnavailable, got %v", err)
		}
		if len(s.created) != 0 || len(s.caps) != 0 {
			t.Fatalf("nothing may be inserted: created=%d caps=%v", len(s.created), s.caps)
		}
	})

	t.Run("no package, or a package without a domain limit: plain insert", func(t *testing.T) {
		noPkg := &models.User{ID: "u1", Username: &uname}
		for name, d := range map[string]CreateDeps{
			"no package":    deps(newCreateStore(existing), noPkg, packages(2)),
			"max_domains 0": deps(newCreateStore(existing), withPkg, packages(0)),
		} {
			s := d.Domains.(*createStore)
			if _, err := Create(ctx, d, CreateHooks{}, in); err != nil {
				t.Fatalf("%s: create: %v", name, err)
			}
			if len(s.caps) != 0 || len(s.created) != 1 {
				t.Fatalf("%s: want a plain insert, caps=%v created=%d", name, s.caps, len(s.created))
			}
		}
	})
}
