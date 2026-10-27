// app_cache_doctor_cmd.go — GH #605. Health-drift detection (+ optional
// auto-repair) for cache-enabled WordPress installs. `cache_enabled=true` in
// the DB is treated as truth, but the cache silently breaks when the plugin is
// deactivated, the object-cache drop-in or JABALI_CACHE_* constants are gone,
// the Redis ACL user/keyspace drifts, or the socket is unreachable. This sweeps
// every cache-enabled install, asks the agent to run `wp jabali-cache verify`
// (which exercises the whole stack as the tenant), and reports the drifted
// ones. With --repair it re-runs the panel's idempotent SetApplicationCache —
// the exact enable path — to re-stamp constants, re-provision the ACL, and
// re-verify. Operator-run (manual or a systemd timer); no reconciler hot-loop.
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

func newAppCacheDoctorCmd() *cobra.Command {
	var repair, jsonOut, migrateACL bool
	cmd := &cobra.Command{
		Use:     "cache-doctor",
		Short:   "Detect (and optionally --repair) drift on cache-enabled WordPress installs",
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

			type report struct {
				InstallID string `json:"install_id"`
				Path      string `json:"path"`
				Healthy   bool   `json:"healthy"`
				Detail    string `json:"detail,omitempty"`
				Repaired  bool   `json:"repaired,omitempty"`
				RepairErr string `json:"repair_error,omitempty"`
			}
			var reports []report
			checked, drifted, repaired, failed := 0, 0, 0, 0

			// cfg is only needed for --repair; build it lazily so a plain check
			// never requires Redis/secret wiring.
			var repairCfg api.ApplicationHandlerConfig
			if repair || migrateACL {
				repairCfg, err = buildAppDeps()
				if err != nil {
					return fmt.Errorf("wire app deps for --repair: %w", err)
				}
			}

			for i := range installs {
				in := installs[i]
				if in.AppType != "wordpress" || !in.CacheEnabled || in.Status != "ready" {
					continue
				}
				dom, dErr := domRepo.FindByID(ctx, in.DomainID)
				if dErr != nil || dom == nil || dom.DocRoot == "" {
					continue
				}
				u, uErr := userRepo.FindByID(ctx, in.UserID)
				if uErr != nil || u == nil || u.Username == nil || *u.Username == "" {
					continue
				}
				installPath := dom.DocRoot
				if in.Subdirectory != "" {
					installPath = path.Join(dom.DocRoot, in.Subdirectory)
				}
				checked++

				callCtx, callCancel := context.WithTimeout(ctx, 90*time.Second)
				raw, hErr := sharedAgent.Call(callCtx, "wordpress.cache_health", map[string]any{
					"install_path": installPath,
					"os_user":      *u.Username,
				})
				callCancel()

				r := report{InstallID: in.ID, Path: installPath, Healthy: true}
				if hErr != nil {
					// Agent transport failure — can't determine health; flag it.
					r.Healthy = false
					r.Detail = "health check failed: " + hErr.Error()
				} else {
					var hv struct {
						Healthy bool   `json:"healthy"`
						Detail  string `json:"detail"`
					}
					_ = json.Unmarshal(raw, &hv)
					r.Healthy = hv.Healthy
					r.Detail = hv.Detail
				}

				if !r.Healthy {
					drifted++
					if repair {
						rErr := api.SetApplicationCache(ctx, repairCfg, in.ID, true, true, "")
						if rErr != nil {
							r.RepairErr = rErr.Error()
							failed++
						} else {
							r.Repaired = true
							repaired++
						}
					}
				}
				reports = append(reports, r)
			}

			if jsonOut {
				_ = printJSON(map[string]any{
					"checked": checked, "drifted": drifted,
					"repaired": repaired, "repair_failed": failed,
					"installs": reports,
				})
			} else {
				for _, r := range reports {
					if r.Healthy {
						continue
					}
					line := fmt.Sprintf("DRIFT %s (%s): %s", r.InstallID, r.Path, r.Detail)
					if repair {
						if r.Repaired {
							line += "  -> REPAIRED"
						} else {
							line += "  -> REPAIR FAILED: " + r.RepairErr
						}
					}
					fmt.Println(line)
				}
				fmt.Printf("done: %d checked, %d drifted, %d repaired, %d repair-failed\n",
					checked, drifted, repaired, failed)
			}

			if migrateACL {
				// JAB-62: force re-provision EVERY cache-enabled install to a per-install
				// ACL user (drift-repair only touches unhealthy installs; a healthy
				// install on the legacy shared user must still migrate). Then reap the
				// orphaned shared user, but ONLY after ALL of that user's installs
				// re-provisioned this run — else a not-yet-migrated sibling loses cache.
				perUserTotal := map[string]int{}
				perUserOK := map[string]int{}
				migFailed := 0
				for i := range installs {
					in := installs[i]
					if in.AppType != "wordpress" || !in.CacheEnabled || in.Status != "ready" {
						continue
					}
					u, uErr := userRepo.FindByID(ctx, in.UserID)
					if uErr != nil || u == nil || u.Username == nil || *u.Username == "" {
						continue
					}
					osUser := *u.Username
					perUserTotal[osUser]++
					if rErr := api.SetApplicationCache(ctx, repairCfg, in.ID, true, true, ""); rErr != nil {
						migFailed++
						fmt.Printf("MIGRATE-ACL FAILED %s (%s): %v\n", in.ID, osUser, rErr)
					} else {
						perUserOK[osUser]++
					}
				}
				reaped, skipped := 0, 0
				for osUser, total := range perUserTotal {
					if perUserOK[osUser] == total {
						if rErr := api.ReapLegacySharedCacheACL(ctx, repairCfg.Redis, osUser); rErr != nil {
							fmt.Printf("reap wp_%s: %v\n", osUser, rErr)
						} else {
							reaped++
							cliAuditOK(ctx, "cache_acl_reap_shared", "cache_acl", "wp_"+osUser, nil)
						}
					} else {
						skipped++
						fmt.Printf("SKIP reap wp_%s: %d/%d installs migrated (leaving shared user for un-migrated sibling)\n", osUser, perUserOK[osUser], total)
					}
				}
				fmt.Printf("migrate-acl: %d users, %d reaped, %d skipped, %d install re-provision failures\n", len(perUserTotal), reaped, skipped, migFailed)
				if migFailed > 0 {
					return fmt.Errorf("%d install(s) failed ACL migration", migFailed)
				}
			}

			cliAuditOK(ctx, "app.cache_doctor", "app_install", "*", nil)
			if failed > 0 {
				return fmt.Errorf("%d install(s) failed to repair", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "re-provision drifted installs (re-stamp constants, re-provision ACL, re-verify)")
	cmd.Flags().BoolVar(&migrateACL, "migrate-acl", false, "re-provision every cache-enabled install to a per-install Redis ACL user, then reap orphaned legacy shared users (safe/idempotent)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit a JSON report")
	return cmd
}
