// app_cache_doctor_run_cmd.go — JAB-11 fleet follow-up. `jabali app
// cache-doctor-run` runs the SAME fleet sweep as the admin GUI's
// POST /admin/cache/doctor-runs (api.SweepCacheDoctor) and persists a
// cache_doctor_runs row. The jabali-cache-doctor.timer runs this nightly so
// standing drift shows up in the admin Cache page's run history without anyone
// clicking "Run doctor". (The older `cache-doctor` command prints to stdout and
// does not record a run; this one records.)
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newAppCacheDoctorRunCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "cache-doctor-run",
		Short: "Run a recorded fleet cache-doctor sweep (persists a run for the admin GUI history)",
		Long: `Sweep every cache-enabled WordPress install and record the result as a
cache_doctor_runs row visible in the admin Cache page. --kind doctor (default)
detects drift read-only; repair re-provisions drifted installs; refresh updates
the jabali-cache plugin fleet-wide. Runs the same executor as the GUI trigger.
Invoked nightly by jabali-cache-doctor.timer.`,
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch kind {
			case models.CacheDoctorKindDoctor, models.CacheDoctorKindRepair, models.CacheDoctorKindRefresh:
			default:
				return fmt.Errorf("--kind must be doctor, repair, or refresh")
			}

			cfg, err := buildAppDeps()
			if err != nil {
				return fmt.Errorf("wire app deps: %w", err)
			}
			if kind == models.CacheDoctorKindRepair && cfg.Redis == nil {
				return fmt.Errorf("repair needs Redis wiring (JABALI_REDIS_* not configured)")
			}
			cfg.CacheDoctorRuns = repository.NewCacheDoctorRunRepository(sharedDB)

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()

			run := &models.CacheDoctorRun{
				ID:          ids.NewULID(),
				Kind:        kind,
				State:       models.CacheDoctorStateQueued,
				TriggeredBy: "", // timer/CLI — no interactive actor
			}
			if err := cfg.CacheDoctorRuns.Create(ctx, run); err != nil {
				return fmt.Errorf("create run: %w", err)
			}
			if err := api.SweepCacheDoctor(ctx, cfg, run, ""); err != nil {
				return fmt.Errorf("sweep: %w", err)
			}
			fmt.Printf("cache-doctor-run %s (%s): checked %d/%d, drifted %d, repaired %d, refreshed %d, failed %d\n",
				run.ID, run.Kind, run.Checked, run.Total, run.Drifted, run.Repaired, run.Refreshed, run.Failed)
			if run.FirstError != "" {
				fmt.Printf("  first error: %s\n", run.FirstError)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", models.CacheDoctorKindDoctor, "sweep kind: doctor | repair | refresh")
	return cmd
}
