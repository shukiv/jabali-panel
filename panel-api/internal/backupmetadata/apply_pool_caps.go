package backupmetadata

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
)

// restoredPoolTuning puts the process settings of a PHP pool from an uploaded
// file through the checks a tenant's pool edit runs (GH #1993): the pool
// page's limits, then the account's package cap. A setting the pool page
// would refuse gives way to the default. It returns a note for each change.
// An error means the cap couldn't be read, and the pool is not restored.
func restoredPoolTuning(ctx context.Context, d Deps, userID string, pool *models.PHPPool) ([]string, error) {
	limit, err := packageFPMCap(ctx, d, userID)
	if err != nil {
		return nil, err
	}
	var notes []string
	t, msg, _, ok := phppoolops.ResolveCreateTuning(false, phppoolops.CreateTuning{
		PmMode:                    pool.PmMode,
		PmMaxChildren:             pool.PmMaxChildren,
		ProcessIdleTimeoutSeconds: pool.ProcessIdleTimeoutSeconds,
	})
	if !ok {
		notes = append(notes, fmt.Sprintf("process settings not restored (%s); the pool uses the defaults", msg))
		t, _, _, _ = phppoolops.ResolveCreateTuning(false, phppoolops.CreateTuning{})
	}
	if limit > 0 && t.PmMaxChildren > limit {
		if msg, ok := phppoolops.ClampToPackageCap(limit, t.PmMode, &t.PmMaxChildren, &t.PmStartServers,
			&t.PmMinSpareServers, &t.PmMaxSpareServers, &t.PmMaxRequests, &t.RequestTerminateTimeoutSeconds); !ok {
			return nil, fmt.Errorf("its settings don't fit the package's cap: %s", msg)
		}
		notes = append(notes, fmt.Sprintf("max children lowered to %d, the account's package cap", limit))
	}
	pool.PmMode, pool.PmMaxChildren, pool.ProcessIdleTimeoutSeconds = t.PmMode, t.PmMaxChildren, t.ProcessIdleTimeoutSeconds
	pool.PmStartServers, pool.PmMinSpareServers, pool.PmMaxSpareServers = t.PmStartServers, t.PmMinSpareServers, t.PmMaxSpareServers
	return notes, nil
}

// packageFPMCap is the FPM max-children cap of the account's package, or 0
// for an account with no package (no package cap).
func packageFPMCap(ctx context.Context, d Deps, userID string) (uint32, error) {
	if d.Users == nil || d.Packages == nil {
		return 0, errors.New("the package checks are not wired")
	}
	u, err := d.Users.FindByID(ctx, userID)
	if err != nil || u == nil {
		return 0, fmt.Errorf("look up the account: %v", err)
	}
	if u.PackageID == nil || *u.PackageID == "" {
		return 0, nil
	}
	pkg, err := d.Packages.FindByID(ctx, *u.PackageID)
	if err != nil || pkg == nil {
		return 0, fmt.Errorf("look up the account's package: %v", err)
	}
	return pkg.FpmMaxChildrenCap, nil
}
