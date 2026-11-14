// Package limitsync computes one tenant's resource-limits plan from stored
// state — its package, its limit override and the disk-quota toggle — the
// same plan the limits reconciler converges to (internal/limits.PlanFor).
// Callers that change what the plan depends on use it to converge one tenant
// at once instead of waiting for the next reconcile pass.
package limitsync

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// UserFinder loads a user by id.
type UserFinder interface {
	FindByID(ctx context.Context, id string) (*models.User, error)
}

// PackageFinder loads a hosting package by id.
type PackageFinder interface {
	FindByID(ctx context.Context, id string) (*models.HostingPackage, error)
}

// OverrideFinder loads a user's limit override; repository.ErrNotFound when
// the user has none.
type OverrideFinder interface {
	FindByUserID(ctx context.Context, userID string) (*models.UserLimitOverride, error)
}

// SettingsGetter reads the server settings (the disk-quota toggle).
type SettingsGetter interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
}

// Tenant computes plans from stored state. Unlike the reconciler, which
// carries on with what it can read, every lookup failure here is an error:
// its callers act on the plan right away and must not act on a guess.
type Tenant struct {
	Users     UserFinder
	Packages  PackageFinder
	Overrides OverrideFinder
	Settings  SettingsGetter
}

// PlanFor returns userID's plan on quotaMount, the mount /home lives on
// (limits.QuotaMountFor), with the given isolated FTP/SFTP account shares.
func (t Tenant) PlanFor(ctx context.Context, userID, quotaMount string, shares limits.IsolatedShares) (limits.LimitsPlan, error) {
	if t.Users == nil || t.Packages == nil || t.Overrides == nil || t.Settings == nil {
		return limits.LimitsPlan{}, errors.New("limitsync: not configured")
	}
	u, err := t.Users.FindByID(ctx, userID)
	if err != nil {
		return limits.LimitsPlan{}, fmt.Errorf("limitsync: user %s: %w", userID, err)
	}
	var pkgL *limits.PackageLimits
	if u.PackageID != nil && *u.PackageID != "" {
		pkg, err := t.Packages.FindByID(ctx, *u.PackageID)
		if err != nil {
			return limits.LimitsPlan{}, fmt.Errorf("limitsync: package %s: %w", *u.PackageID, err)
		}
		pkgL = &limits.PackageLimits{
			DiskQuotaMB:     pkg.DiskQuotaMB,
			CPUQuotaPercent: pkg.CPUQuotaPercent,
			MemoryLimitMB:   pkg.MemoryLimitMB,
			IOReadMbps:      pkg.IOReadMbps,
			IOWriteMbps:     pkg.IOWriteMbps,
			MaxTasks:        pkg.MaxTasks,
		}
	}
	var ovL *limits.OverrideLimits
	ov, err := t.Overrides.FindByUserID(ctx, userID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
	case err != nil:
		return limits.LimitsPlan{}, fmt.Errorf("limitsync: limit override of %s: %w", userID, err)
	case ov != nil:
		ovL = &limits.OverrideLimits{
			DiskQuotaMB:     ov.DiskQuotaMB,
			CPUQuotaPercent: ov.CPUQuotaPercent,
			MemoryLimitMB:   ov.MemoryLimitMB,
			IOReadMbps:      ov.IOReadMbps,
			IOWriteMbps:     ov.IOWriteMbps,
			MaxTasks:        ov.MaxTasks,
		}
	}
	s, err := t.Settings.Get(ctx)
	if err != nil {
		return limits.LimitsPlan{}, fmt.Errorf("limitsync: server settings: %w", err)
	}
	quotaEnabled := true
	if s != nil {
		quotaEnabled = s.DiskQuotaEnabled
	}
	return limits.PlanFor(pkgL, ovL, quotaEnabled, quotaMount, shares), nil
}
