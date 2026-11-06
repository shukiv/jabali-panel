package dbops

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ErrQuotaUnavailable reports that the owner's hosting package is set but
// could not be loaded, so its limits cannot be checked. Creates refuse on it.
var ErrQuotaUnavailable = errors.New("dbops: hosting package limits cannot be loaded")

// PackageLimit returns pick(pkg) for the user's hosting package; 0 means no
// cap. No package, or a package id that no longer exists (a deleted package
// leaves its id on the user), is no cap: no package = unrestricted for
// ordinary limits (GH #282). Any other lookup failure, or no package
// repository, is ErrQuotaUnavailable: a cap must not open up because it could
// not be read.
func PackageLimit(ctx context.Context, pkgs repository.PackageRepository, user *models.User, pick func(*models.HostingPackage) uint32) (uint32, error) {
	if user.PackageID == nil || *user.PackageID == "" {
		return 0, nil
	}
	if pkgs == nil {
		return 0, fmt.Errorf("%w: no package repository", ErrQuotaUnavailable)
	}
	pkg, err := pkgs.FindByID(ctx, *user.PackageID)
	if errors.Is(err, repository.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrQuotaUnavailable, err)
	}
	if pkg == nil {
		return 0, fmt.Errorf("%w: package %s loaded as nil", ErrQuotaUnavailable, *user.PackageID)
	}
	return pick(pkg), nil
}

// MaxDatabases and MaxDatabaseUsers pick a limit for PackageLimit.
func MaxDatabases(p *models.HostingPackage) uint32     { return p.MaxDatabases }
func MaxDatabaseUsers(p *models.HostingPackage) uint32 { return p.MaxDatabaseUsers }
