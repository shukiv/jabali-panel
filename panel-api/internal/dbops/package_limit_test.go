package dbops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type limitPkgs struct {
	repository.PackageRepository
	pkg *models.HostingPackage
	err error
}

func (f limitPkgs) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, f.err
}

func TestPackageLimit(t *testing.T) {
	pkgID := "pkg1"
	packaged := &models.User{ID: "u1", PackageID: &pkgID}
	for _, tc := range []struct {
		name    string
		user    *models.User
		pkgs    repository.PackageRepository
		want    uint32
		wantErr error
	}{
		{"no package", &models.User{ID: "u1"}, nil, 0, nil},
		{"package limit", packaged, limitPkgs{pkg: &models.HostingPackage{MaxDatabaseUsers: 4}}, 4, nil},
		{"package without that limit", packaged, limitPkgs{pkg: &models.HostingPackage{}}, 0, nil},
		{"deleted package", packaged, limitPkgs{err: repository.ErrNotFound}, 0, nil},
		{"lookup error", packaged, limitPkgs{err: errors.New("db down")}, 0, ErrQuotaUnavailable},
		{"nil package", packaged, limitPkgs{}, 0, ErrQuotaUnavailable},
		{"no repository", packaged, nil, 0, ErrQuotaUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PackageLimit(context.Background(), tc.pkgs, tc.user, MaxDatabaseUsers)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) || got != tc.want {
				t.Fatalf("got %d, %v; want %d, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
