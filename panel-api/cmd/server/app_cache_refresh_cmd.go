// app_cache_refresh_cmd.go — GH #613 follow-up. Sweep every cache-enabled
// WordPress install and update the jabali-cache plugin to the latest
// WordPress.org release. `jabali update` calls this so existing sites converge
// to the published version without a manual cache re-toggle (the per-enable
// WP.org install in wordpress.cache_set only touches sites when cache is
// toggled). Idempotent + best-effort: a site already current is a no-op, a
// site without the plugin is skipped, a single failure never aborts the sweep.
//
// After each refresh to a plugin that flushes without SCAN (1.2.0+), the
// sweep re-applies that site's Redis ACL rule, which no longer grants SCAN
// (ADR-0173). Sites provisioned before that change keep their old rule until
// then.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cachePluginRefreshResult is the agent's wordpress.cache_plugin_refresh reply.
type cachePluginRefreshResult struct {
	Refreshed bool   `json:"refreshed"`
	Version   string `json:"version"`
}

// resyncInstallACL is api.ResyncInstallACL; a seam for tests.
var resyncInstallACL = api.ResyncInstallACL

// refreshResyncACL re-applies a refreshed site's Redis ACL rule when its new
// plugin flushes without SCAN. It only ever tightens the rule: any other
// version, or no Redis on this host, leaves the ACL as it is. (By default the
// agent reports the version of the bundle it staged, not anything read from
// the site.)
func refreshResyncACL(ctx context.Context, cfg api.ApplicationHandlerConfig, res cachePluginRefreshResult, userID, osUser, installID string) (bool, error) {
	if !res.Refreshed || !api.CachePluginFlushesWithoutScan(res.Version) || cfg.Redis == nil {
		return false, nil
	}
	if err := resyncInstallACL(ctx, cfg, userID, osUser, installID); err != nil {
		return false, err
	}
	return true, nil
}

func newAppRefreshCachePluginCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "refresh-cache-plugin",
		Short:   "Re-install the bundled jabali-cache plugin on every cache-enabled WordPress site",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()

			instRepo := repository.NewApplicationInstallRepository(sharedDB)
			domRepo := domainRepoFromDB()
			userRepo := repository.NewUserRepository(sharedDB)

			installs, _, err := instRepo.List(ctx, repository.ListOptions{Limit: 100000})
			if err != nil {
				return fmt.Errorf("list installs: %w", err)
			}

			// Redis + the cache token secret/salts for the ACL re-sync; without
			// Redis the re-sync is skipped and the sweep only refreshes.
			cacheCfg, _ := buildAppDeps()

			refreshed, skipped, failed, aclSynced, aclFailed := 0, 0, 0, 0, 0
			for i := range installs {
				in := installs[i]
				if in.AppType != "wordpress" || !in.CacheEnabled || in.Status != "ready" {
					continue
				}
				dom, dErr := domRepo.FindByID(ctx, in.DomainID)
				if dErr != nil || dom == nil || dom.DocRoot == "" {
					fmt.Printf("  skip %s: domain/docroot unresolved\n", in.ID)
					skipped++
					continue
				}
				u, uErr := userRepo.FindByID(ctx, in.UserID)
				if uErr != nil || u == nil || u.Username == nil || *u.Username == "" {
					fmt.Printf("  skip %s: no linux username\n", in.ID)
					skipped++
					continue
				}
				installPath := dom.DocRoot
				if in.Subdirectory != "" {
					installPath = path.Join(dom.DocRoot, in.Subdirectory)
				}

				callCtx, callCancel := context.WithTimeout(ctx, 3*time.Minute)
				raw, cErr := sharedAgent.Call(callCtx, "wordpress.cache_plugin_refresh", map[string]any{
					"install_path": installPath,
					"os_user":      *u.Username,
				})
				callCancel()
				if cErr != nil {
					fmt.Printf("  FAIL %s (%s): %v\n", in.ID, installPath, cErr)
					failed++
					continue
				}
				var res cachePluginRefreshResult
				_ = json.Unmarshal(raw, &res)
				if res.Refreshed {
					fmt.Printf("  refreshed %s (%s) -> %s\n", in.ID, installPath, res.Version)
					refreshed++
					synced, aErr := refreshResyncACL(ctx, cacheCfg, res, in.UserID, *u.Username, in.ID)
					switch {
					case aErr != nil:
						fmt.Printf("  FAIL %s: re-sync the site's Redis ACL: %v\n", in.ID, aErr)
						aclFailed++
					case synced:
						aclSynced++
					}
				} else {
					skipped++
				}
			}

			fmt.Printf("done: %d refreshed, %d skipped, %d failed; %d Redis ACLs re-synced, %d failed\n", refreshed, skipped, failed, aclSynced, aclFailed)
			cliAuditOK(ctx, "app.refresh_cache_plugin", "app_install", "*", nil)
			if failed > 0 {
				return fmt.Errorf("%d site(s) failed to refresh", failed)
			}
			if aclFailed > 0 {
				return fmt.Errorf("%d site(s) failed to re-sync their Redis ACL (they keep the previous rule)", aclFailed)
			}
			return nil
		},
	}
}
