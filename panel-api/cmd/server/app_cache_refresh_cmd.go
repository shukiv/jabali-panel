// app_cache_refresh_cmd.go — GH #613 follow-up. Sweep every cache-enabled
// WordPress install and update the jabali-cache plugin to the latest
// WordPress.org release. `jabali update` calls this so existing sites converge
// to the published version without a manual cache re-toggle (the per-enable
// WP.org install in wordpress.cache_set only touches sites when cache is
// toggled). Idempotent + best-effort: a site already current is a no-op, a
// site without the plugin is skipped, a single failure never aborts the sweep.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

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

			refreshed, skipped, failed := 0, 0, 0
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
				var res struct {
					Refreshed bool   `json:"refreshed"`
					Version   string `json:"version"`
				}
				_ = json.Unmarshal(raw, &res)
				if res.Refreshed {
					fmt.Printf("  refreshed %s (%s) -> %s\n", in.ID, installPath, res.Version)
					refreshed++
				} else {
					skipped++
				}
			}

			fmt.Printf("done: %d refreshed, %d skipped, %d failed\n", refreshed, skipped, failed)
			cliAuditOK(ctx, "app.refresh_cache_plugin", "app_install", "*", nil)
			if failed > 0 {
				return fmt.Errorf("%d site(s) failed to refresh", failed)
			}
			return nil
		},
	}
}
