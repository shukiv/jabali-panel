package api

import (
	"context"
	"errors"
	"log/slog"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// flarumWAFDeps gathers what appsecops.SyncFlarum needs. A nil store stays nil,
// and SyncFlarum then reports ErrNotConfigured and does nothing.
func flarumWAFDeps(ag agent.AgentInterface, excl repository.CRSRuleExclusionRepository,
	modes repository.CRSHostModeRepository, installs repository.ApplicationInstallRepository,
	domains repository.DomainRepository) appsecops.Deps {
	return appsecops.Deps{Agent: ag, Exclusions: excl, HostModes: modes, Installs: installs, Domains: domains}
}

// syncFlarumWAF runs appsecops.SyncFlarum for a Flarum install (GH #1650):
// installID after an install, "" after a delete. It only logs. A failure must
// never fail the install or the delete; the next sync, the panel's start-up
// prune, or `jabali appsec render-config --reconcile --reload` catches up.
func syncFlarumWAF(ctx context.Context, d appsecops.Deps, installID string) {
	res, err := appsecops.SyncFlarum(ctx, d, installID)
	switch {
	case errors.Is(err, appsecops.ErrNotConfigured):
		return
	case err != nil:
		slog.WarnContext(ctx, "flarum: WAF exclusion sync failed — CRS 920450 may block the forum's API until the next sync",
			"err", err, "install_id", installID)
	default:
		slog.InfoContext(ctx, "flarum: WAF exclusion synced",
			"install_id", installID, "changed", res.Changed, "reloaded", res.Reloaded, "skipped", res.Skipped)
	}
}
