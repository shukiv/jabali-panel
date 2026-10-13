package api

import (
	"context"
	"errors"
	"log/slog"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// appWAFDeps gathers what appsecops.SyncInstall needs. A nil store stays nil,
// and SyncInstall then reports ErrNotConfigured and does nothing.
func appWAFDeps(ag agent.AgentInterface, excl repository.CRSRuleExclusionRepository,
	modes repository.CRSHostModeRepository, installs repository.ApplicationInstallRepository,
	domains repository.DomainRepository) appsecops.Deps {
	return appsecops.Deps{Agent: ag, Exclusions: excl, HostModes: modes, Installs: installs, Domains: domains}
}

// syncAppWAF runs appsecops.SyncInstall for an install of an app that carries
// managed CRS exclusions (Flarum GH #1650, Nextcloud Discussion #1032):
// installID after an install, "" after a delete. It only logs. A failure must
// never fail the install or the delete; the next sync, the panel's start-up
// prune, or `jabali appsec render-config --reconcile --reload` catches up.
func syncAppWAF(ctx context.Context, d appsecops.Deps, appType, installID string) {
	res, err := appsecops.SyncInstall(ctx, d, installID)
	switch {
	case errors.Is(err, appsecops.ErrNotConfigured):
		return
	case err != nil:
		slog.WarnContext(ctx, "app: WAF exclusion sync failed — CRS may block the app's requests until the next sync",
			"err", err, "app", appType, "install_id", installID)
	default:
		slog.InfoContext(ctx, "app: WAF exclusion synced",
			"app", appType, "install_id", installID, "changed", res.Changed, "reloaded", res.Reloaded, "skipped", res.Skipped)
	}
}
