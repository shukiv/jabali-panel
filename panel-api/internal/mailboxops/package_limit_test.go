package mailboxops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// limitMBRepo is fakeMBRepo with the owner count and the capped insert.
type limitMBRepo struct {
	fakeMBRepo
	count     int64
	capErr    error // returned by CreateWithinCap, as when a racing create took the slot
	capOwner  string
	capMax    uint32
	capCalled bool
}

func (f *limitMBRepo) CountByOwner(context.Context, string) (int64, error) { return f.count, nil }

func (f *limitMBRepo) CreateWithinCap(ctx context.Context, mb *models.Mailbox, owner string, max uint32) error {
	f.capCalled, f.capOwner, f.capMax = true, owner, max
	if f.capErr != nil {
		return f.capErr
	}
	return f.fakeMBRepo.Create(ctx, mb)
}

type limitUsers struct {
	repository.UserRepository
	pkg *string
}

func (u limitUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	return &models.User{ID: id, PackageID: u.pkg}, nil
}

type limitPackages struct {
	repository.PackageRepository
	max uint32
	err error
}

func (p limitPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &models.HostingPackage{ID: id, MaxEmailAccounts: p.max}, nil
}

func ownedDomain() *models.Domain {
	d := enabledDomain()
	d.UserID = "u1"
	return d
}

func createWithPackage(repo *limitMBRepo, users repository.UserRepository, pkgs repository.PackageRepository) (*models.Mailbox, error) {
	key := ssokey.Key{}
	mb, _, err := Create(context.Background(),
		Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}, Users: users, Packages: pkgs},
		CreateInput{Domain: ownedDomain(), LocalPart: "alice"}, nil)
	return mb, err
}

func pkgID() *string { s := "p1"; return &s }

// The package's email-account limit was stored and shown but never applied:
// any number of mailboxes could be created.
func TestCreate_PackageEmailAccountLimitReached(t *testing.T) {
	repo := &limitMBRepo{count: 2}
	_, err := createWithPackage(repo, limitUsers{pkg: pkgID()}, limitPackages{max: 2})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
	if repo.created != nil {
		t.Fatal("no row may be written at the limit")
	}
}

// Under the limit the insert itself is held to it, for the owner, so two
// creates racing for the last slot can't both get it.
func TestCreate_UnderTheLimitUsesTheCappedInsert(t *testing.T) {
	repo := &limitMBRepo{count: 1}
	if _, err := createWithPackage(repo, limitUsers{pkg: pkgID()}, limitPackages{max: 3}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !repo.capCalled || repo.capOwner != "u1" || repo.capMax != 3 {
		t.Fatalf("capped insert called=%v owner=%q max=%d, want true u1 3", repo.capCalled, repo.capOwner, repo.capMax)
	}
	if repo.created == nil {
		t.Fatal("no row written")
	}
}

func TestCreate_TheCappedInsertRefusalIsTheLimit(t *testing.T) {
	repo := &limitMBRepo{count: 1, capErr: repository.ErrMailboxCapExceeded}
	_, err := createWithPackage(repo, limitUsers{pkg: pkgID()}, limitPackages{max: 2})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
}

// No package, or a package that sets 0, is no limit (GH #282).
func TestCreate_NoPackageOrZeroIsUnlimited(t *testing.T) {
	for name, users := range map[string]limitUsers{"no package": {}, "zero": {pkg: pkgID()}} {
		t.Run(name, func(t *testing.T) {
			repo := &limitMBRepo{count: 1000}
			if _, err := createWithPackage(repo, users, limitPackages{max: 0}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if repo.capCalled || repo.created == nil {
				t.Fatalf("want a plain insert, got capped=%v row=%v", repo.capCalled, repo.created != nil)
			}
		})
	}
}

// A package that is set but can't be loaded refuses the create instead of
// lifting the limit.
func TestCreate_PackageNotLoadableRefuses(t *testing.T) {
	repo := &limitMBRepo{}
	_, err := createWithPackage(repo, limitUsers{pkg: pkgID()}, limitPackages{err: errors.New("db down")})
	if !errors.Is(err, ErrQuotaUnavailable) {
		t.Fatalf("want ErrQuotaUnavailable, got %v", err)
	}
	if repo.created != nil {
		t.Fatal("no row may be written")
	}
}
