package backupmetadata

import (
	"context"
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
	t, refused, notes, err := poolTuning(limit, pool.PmMode, pool.PmMaxChildren, pool.ProcessIdleTimeoutSeconds)
	if err == nil && refused != "" {
		notes = []string{fmt.Sprintf("process settings not restored (%s); the pool uses the defaults", refused)}
		var capped []string
		t, _, capped, err = poolTuning(limit, "", 0, 0)
		notes = append(notes, capped...)
	}
	if err != nil {
		return nil, err
	}
	setPoolTuning(pool, t)
	return notes, nil
}

// poolTuning holds process settings to the pool page's limits, then to the
// package cap limit (0: none), with a note for each change. refused is the
// pool page's reason when it would refuse them. An error means they don't fit
// the cap.
func poolTuning(limit uint32, mode string, children, idle uint32) (t phppoolops.CreateTuning, refused string, notes []string, err error) {
	t, msg, _, ok := phppoolops.ResolveCreateTuning(false, phppoolops.CreateTuning{
		PmMode:                    mode,
		PmMaxChildren:             children,
		ProcessIdleTimeoutSeconds: idle,
	})
	if !ok {
		return t, msg, nil, nil
	}
	if limit > 0 && t.PmMaxChildren > limit {
		if msg, ok := phppoolops.ClampToPackageCap(limit, t.PmMode, &t.PmMaxChildren, &t.PmStartServers,
			&t.PmMinSpareServers, &t.PmMaxSpareServers, &t.PmMaxRequests, &t.RequestTerminateTimeoutSeconds); !ok {
			return t, "", nil, fmt.Errorf("its settings don't fit the package's cap: %s", msg)
		}
		notes = append(notes, fmt.Sprintf("max children lowered to %d, the account's package cap", limit))
	}
	return t, "", notes, nil
}

// setPoolTuning puts the process settings t on pool.
func setPoolTuning(pool *models.PHPPool, t phppoolops.CreateTuning) {
	pool.PmMode, pool.PmMaxChildren, pool.ProcessIdleTimeoutSeconds = t.PmMode, t.PmMaxChildren, t.ProcessIdleTimeoutSeconds
	pool.PmStartServers, pool.PmMinSpareServers, pool.PmMaxSpareServers = t.PmStartServers, t.PmMinSpareServers, t.PmMaxSpareServers
}

// packageFPMCap is the FPM max-children cap of the account's package, or 0
// for an account with no package (no package cap).
func packageFPMCap(ctx context.Context, d Deps, userID string) (uint32, error) {
	pkg, err := accountPackage(ctx, d, userID)
	if err != nil || pkg == nil {
		return 0, err
	}
	return pkg.FpmMaxChildrenCap, nil
}
