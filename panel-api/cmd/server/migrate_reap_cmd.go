// `jabali migrate reap-secrets` cobra subcommand. Walks
// migration_jobs WHERE state IN ('done','failed','cancelled') +
// deletes the per-job env file at /etc/jabali-panel/migration-
// secrets/<job-id>.env (if present).
//
// Closes the M35 ADR-0094 §"tracked risks" gap: per-job source
// credentials previously persisted across job-terminal state with
// no scheduled wipe. Reaper run by jabali-migration-secrets-reap.timer
// (daily 04:30 UTC + jitter), idempotent; missing files are
// non-fatal.
//
// Operator can run on demand via: jabali migrate reap-secrets
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	migrationSecretsDir = "/etc/jabali-panel/migration-secrets"
	migrationStagingDir = "/var/lib/jabali-migrations"
	// migrationStagingMaxAge — terminal jobs whose tarball + extracted
	// tree have been on disk longer than this get reaped. 7 days gives
	// the operator a generous window to download the cpmove for forensics
	// or re-attempt a manual fixup before disk goes away. Override via
	// `--staging-max-age` flag for one-shot operator runs.
	migrationStagingMaxAge = 7 * 24 * time.Hour
)

func newMigrateReapSecretsCmd() *cobra.Command {
	var dryRun bool
	var stagingMaxAge time.Duration
	cmd := &cobra.Command{
		Use:   "reap-secrets",
		Short: "Wipe per-job migration-secrets env files + stale tarball/extracted dirs",
		Long: `Walks migration_jobs WHERE state IN
('done','failed','cancelled') and deletes the matching env file
at /etc/jabali-panel/migration-secrets/<job-id>.env. Same pass
removes the per-job tarball + extracted tree under
/var/lib/jabali-migrations/<id>/ when the job's ended_at is older
than --staging-max-age (default 7 days). Idempotent — missing
files don't fail. Run by jabali-migration-secrets-reap.timer on a
daily cadence; operator can also invoke directly.`,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			repo := repository.NewMigrationJobRepository(sharedDB)
			pageSize := 200
			page := 1
			deleted := 0
			scanned := 0
			// JAB-155: track every job ID that still has a row so the orphan
			// sweep below can tell a stranded (row-deleted) staging dir from
			// a live one.
			live := map[string]struct{}{}
			for {
				rows, total, err := repo.List(ctx, page, pageSize)
				if err != nil {
					return fmt.Errorf("list migration jobs: %w", err)
				}
				for _, row := range rows {
					scanned++
					live[row.ID] = struct{}{}
					if !isTerminal(row.State) {
						continue
					}
					path := filepath.Join(migrationSecretsDir, row.ID+".env")
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						if dryRun {
							fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would remove %s (job state=%s)\n", path, row.State)
							deleted++
						} else if err := os.Remove(path); err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "remove %s: %v\n", path, err)
						} else {
							deleted++
						}
					}
					// Staging dir: tarball + extracted tree. Reap when
					// the job has been terminal long enough (ended_at +
					// stagingMaxAge < now). Operator can `--staging-max-age 0`
					// to wipe immediately.
					stagingPath := filepath.Join(migrationStagingDir, row.ID)
					if info, sErr := os.Stat(stagingPath); sErr == nil && info.IsDir() {
						ref := row.EndedAt
						if ref == nil || ref.IsZero() {
							ref = &row.UpdatedAt
						}
						age := time.Since(*ref)
						if age < stagingMaxAge {
							continue
						}
						if dryRun {
							fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would rm -rf %s (state=%s age=%s)\n", stagingPath, row.State, age.Truncate(time.Hour))
							deleted++
							continue
						}
						if rErr := os.RemoveAll(stagingPath); rErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "rm -rf %s: %v\n", stagingPath, rErr)
							continue
						}
						deleted++
					}
				}
				if int64(page*pageSize) >= total || len(rows) == 0 {
					break
				}
				page++
			}

			// JAB-155: orphan sweep. The row-driven pass above only reaps
			// staging for jobs that still have a migration_jobs row. When a
			// row is hard-deleted (admin removed the job, or a wizard draft
			// was cleaned), its /var/lib/jabali-migrations/<id>/ tree is left
			// stranded and invisible to that pass — on a test host this
			// stranded ~6GB across 5 dirs and pushed / to 95% full.
			deleted += reapOrphanStaging(migrationStagingDir, live, stagingMaxAge, dryRun, cmd.OutOrStdout(), cmd.ErrOrStderr())

			fmt.Fprintf(cmd.OutOrStdout(), "scanned=%d deleted=%d (dry-run=%v)\n", scanned, deleted, dryRun)
			// ADR-0095 decision 5 — also reap draft migration_jobs
			// older than 24h. Drafts are created by the wizard at Step
			// 1; an operator who closes the tab without finishing
			// leaves a row behind. 24h is a generous "not coming back"
			// window. Hard-delete (no per-job secrets to wipe; secrets
			// only land at Step 2 which flips state out of draft).
			cutoff := time.Now().Add(-24 * time.Hour)
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would reap draft jobs older than %s\n", cutoff.Format(time.RFC3339))
			} else {
				if n, err := repo.CancelDraftsOlderThan(ctx, cutoff); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "reap drafts: %v\n", err)
				} else if n > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "reaped %d draft job(s) older than 24h\n", n)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List would-delete paths without removing them")
	cmd.Flags().DurationVar(&stagingMaxAge, "staging-max-age", migrationStagingMaxAge,
		"Reap /var/lib/jabali-migrations/<id>/ only when the job has been terminal at least this long (default 168h = 7d; pass 0 to wipe immediately)")
	return cmd
}

// reapOrphanStaging removes per-job staging dirs under stagingDir whose ID has
// no entry in live (the set of job IDs that still have a migration_jobs row)
// and whose mtime is older than maxAge. The mtime guard protects an in-flight
// extraction whose job row is not committed yet. Returns the number of dirs
// removed (or, under dryRun, that would be removed). Missing stagingDir is not
// an error. Split out from the command for direct testing. (JAB-155)
func reapOrphanStaging(stagingDir string, live map[string]struct{}, maxAge time.Duration, dryRun bool, out, errw io.Writer) int {
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(errw, "readdir %s: %v\n", stagingDir, err)
		}
		return 0
	}
	deleted := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := live[e.Name()]; ok {
			continue // job row still exists — handled by the row-driven pass
		}
		p := filepath.Join(stagingDir, e.Name())
		info, iErr := e.Info()
		if iErr != nil {
			continue
		}
		age := time.Since(info.ModTime())
		if age < maxAge {
			continue
		}
		if dryRun {
			fmt.Fprintf(out, "[dry-run] would rm -rf %s (orphan: no job row, age=%s)\n", p, age.Truncate(time.Hour))
			deleted++
			continue
		}
		if rmErr := os.RemoveAll(p); rmErr != nil {
			fmt.Fprintf(errw, "rm -rf %s: %v\n", p, rmErr)
			continue
		}
		deleted++
	}
	return deleted
}

func isTerminal(state string) bool {
	switch state {
	// JAB-49: degraded is terminal (ended_at stamped) — its SSH secret + staging
	// must be reaped like done/failed/cancelled.
	case models.MigrationStateDone, models.MigrationStateDegraded, models.MigrationStateFailed, models.MigrationStateCancelled:
		return true
	}
	return false
}
