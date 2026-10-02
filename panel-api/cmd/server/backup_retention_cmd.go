// `jabali backup retention apply` — fired by jabali-backup-retention.timer
// daily at 04:30. Per ADR-0080 each backup writes directly to ONE
// destination, so retention has to walk every (schedule, destination)
// pair and apply the schedule's keep policy to that destination's repo,
// per whole backup job (backup_retention_jobs.go): the job's snapshots
// are kept or forgotten together, by snapshot ID. A single `restic prune`
// per destination is run at the end so blob removal happens once per
// timer fire per repo.
//
// Manual (non-scheduled) backups carry no schedule-id tag and are
// therefore NEVER auto-pruned. Operators who want them gone delete
// them by hand with `restic forget --tag job-id=<id>`.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const resticForgetTimeout = 30 * time.Minute

// resticPasswordFile is a var only so tests can point the retention sweep at a
// throwaway repo.
var resticPasswordFile = "/etc/jabali-panel/restic-repo.password"

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Backup & restore subcommands (M30 — restic-backed; ADR-0075 / 0080)",
	}
	cmd.AddCommand(newBackupRetentionCmd())
	cmd.AddCommand(newBackupAccountRestoreCmd())
	cmd.AddCommand(newBackupDestinationCmd())
	cmd.AddCommand(newBackupScheduleCmd())
	cmd.AddCommand(newBackupSchedulerCmd())
	cmd.AddCommand(newBackupCopyRetiredCmd())
	return cmd
}

func newBackupRetentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Manage restic retention (forget + prune per destination)",
	}
	cmd.AddCommand(newBackupRetentionApplyCmd())
	return cmd
}

func newBackupRetentionApplyCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Run restic forget per (schedule, destination) + prune per destination",
		Long: `For every (enabled schedule, enabled destination) pair where the
schedule has at least one non-NULL keep_{daily,weekly,monthly}, apply the
policy per whole backup job:

    restic --repo <dest.url> snapshots --json --tag schedule-id=<sched.id>

groups the snapshots by job (job-id tag) and by series (one account, or the
system backup, on one host), keeps complete jobs by restic's daily, weekly
and monthly rules on their manifest times, and forgets every snapshot of a
dropped job by ID. A backup is kept or forgotten as one unit, so no restore
point is left partial. A queued or running job, a snapshot without a job-id
tag, and a series with no complete backup are never forgotten. The backup_jobs
row of a forgotten job is deleted once its snapshots are gone.

Then a single ` + "`restic prune`" + ` per destination at the end. Schedules
with all-NULL keep_* are skipped (operator hasn't picked a policy).
Manual backups (ScheduleID NULL) are never pruned.

Wired into systemd timer jabali-backup-retention.timer (daily 04:30)
by install_backup_foundation in install.sh.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), resticForgetTimeout)
			defer cancel()
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			if err := assertResticEnvironment(); err != nil {
				return err
			}
			// GH #331 two-node drill finding: a DR standby's DB is the
			// PRIMARY's replica, so its schedules + destinations point at
			// the primary's repos — including the shared DR channel. A
			// retention pass here would `restic forget --prune` the
			// primary's live backups from the standby. Hard skip.
			if s, serr := repository.NewServerSettingsRepository(sharedDB).Get(ctx); serr == nil && s != nil && s.IsStandby() {
				fmt.Fprintln(cmd.OutOrStdout(),
					"retention sweep skipped: this box is a DR standby — its replicated destinations point at the primary's repositories, and pruning them from here would destroy the primary's backups")
				return nil
			}

			// JAB-98: prune per-job backup log files under
			// /var/lib/jabali-backups/logs older than the retention window.
			// Age-based (mtime) so an in-flight job's log is never removed,
			// and independent of restic policy — manual/one-off jobs write
			// logs too. Runs on every retention pass; skipped under --dry-run.
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "[dry-run] would prune backup job logs older than 90d")
			} else if n, perr := internalbackup.PruneJobLogs(internalbackup.DefaultJobLogRetention); perr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "prune job logs failed: %v\n", perr)
			} else if n > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "pruned %d expired backup job log(s)\n", n)
			}

			schedRepo := repository.NewBackupScheduleRepository(sharedDB)
			destRepo := repository.NewBackupDestinationRepository(sharedDB)
			jobRepo := repository.NewBackupJobRepository(sharedDB)
			scheds, err := schedRepo.List(ctx)
			if err != nil {
				return fmt.Errorf("list backup_schedules: %w", err)
			}

			// Track which destinations had any forget run against them so
			// we only invoke prune where it would have work to do.
			pruneDests := map[string]*models.BackupDestination{}
			// JAB-392: accumulate every forget/prune failure so the sweep exits
			// non-zero AND fires ONE aggregate admin alert, instead of printing
			// to a journal nobody reads and exiting 0 (it did that for 52 days).
			var failures []string
			for _, s := range scheds {
				if !s.Enabled {
					continue
				}
				if s.KeepDaily == nil && s.KeepWeekly == nil && s.KeepMonthly == nil {
					fmt.Fprintf(cmd.OutOrStdout(),
						"schedule %s: no retention policy, skipping\n", s.ID)
					continue
				}
				dests, err := schedRepo.GetDestinations(ctx, s.ID)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"schedule %s: load destinations failed: %v\n", s.ID, err)
					continue
				}
				for i := range dests {
					d := &dests[i]
					if !d.Enabled {
						continue
					}
					if err := forgetForSchedule(ctx, cmd, s, d, jobRepo, dryRun); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(),
							"schedule %s dest %s forget failed: %v\n", s.ID, d.ID, err)
						failures = append(failures, fmt.Sprintf("schedule %s dest %s (%s) forget: %v", s.ID, d.ID, d.Name, err))
						continue
					}
					pruneDests[d.ID] = d
				}
			}

			if len(pruneDests) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(),
					"no (schedule, destination) pairs with retention policy; nothing to forget or prune")
				return nil
			}

			// Resolve any remaining destinations that may have been
			// stale-cached (defensive; pruneDests was populated above).
			_ = destRepo
			for _, d := range pruneDests {
				if err := pruneOneDestination(ctx, cmd, d, dryRun); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"prune dest %s failed: %v\n", d.ID, err)
					failures = append(failures, fmt.Sprintf("prune dest %s (%s): %v", d.ID, d.Name, err))
				}
			}
			if len(failures) > 0 {
				// Alert (best-effort) + exit non-zero. The notification is the
				// operator-visible signal; the exit code makes `systemctl status`
				// and any OnFailure= truthful even when Redis is down (JAB-392).
				publishBackupRetentionFailure(ctx, cmd, failures)
				return fmt.Errorf("retention sweep completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"List the backup jobs that would be forgotten and pass --dry-run to prune; no destructive ops")
	return cmd
}

// retentionExec is the exec seam for the retention sweep's restic invocations,
// so tests can drive the JAB-392 stale-lock recovery without spawning restic.
// Production wiring is exec.CommandContext.
var retentionExec = func(ctx context.Context, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = env
	c.Stdout, c.Stderr = stdout, stderr
	return c.Run()
}

// resticRepoArgs is the shared global-flag prefix every retention restic call
// needs: the repo URL, the password file, and the destination's -o options.
func resticRepoArgs(d *models.BackupDestination) []string {
	args := []string{"--repo", d.URL, "--password-file", resticPasswordFile}
	for _, opt := range backupwrapperhelpers.ResticOptionsFor(d) {
		if opt == "" {
			continue
		}
		args = append(args, "-o", opt)
	}
	return args
}

// runResticWithLockRecovery runs a retention restic command against d and
// recovers from a stale repository lock (JAB-392).
//
// A crashed or OOM-killed `restic prune`/`forget` leaves an exclusive lock that
// restic will NOT auto-remove when it is from the same host and it cannot prove
// the PID is dead (PID reuse) — so ONE dead prune wedges every future
// forget/prune forever. Found live on production: 52 days of silently-failed
// retention, the repo grown to 7,318 snapshots. On the "repository is already
// locked" error we clear STALE locks with `restic unlock` (NEVER --remove-all —
// that would also drop a live concurrent backup's lock and let prune race a
// running backup) and retry the command once. A lock held by a live backup
// survives the stale-only unlock, so the retry fails again and the caller
// reports the failure instead of pruning underneath a running backup.
func runResticWithLockRecovery(ctx context.Context, cmd *cobra.Command, d *models.BackupDestination, args []string) error {
	return runResticWithLockRecoveryTo(ctx, cmd, d, args, cmd.OutOrStdout())
}

// runResticCapture is runResticWithLockRecovery for a command whose stdout the
// sweep reads (restic snapshots --json).
func runResticCapture(ctx context.Context, cmd *cobra.Command, d *models.BackupDestination, args []string) ([]byte, error) {
	var out bytes.Buffer
	err := runResticWithLockRecoveryTo(ctx, cmd, d, args, &out)
	return out.Bytes(), err
}

func runResticWithLockRecoveryTo(ctx context.Context, cmd *cobra.Command, d *models.BackupDestination, args []string, stdout io.Writer) error {
	env := append(os.Environ(), destEnv(d)...)
	var errBuf bytes.Buffer
	// A retry after a stale-lock unlock writes into a fresh buffer, so a
	// captured stdout never holds the failed attempt's partial output.
	var first bytes.Buffer
	err := retentionExec(ctx, env, &first, io.MultiWriter(cmd.ErrOrStderr(), &errBuf), "restic", args...)
	if err == nil {
		_, werr := stdout.Write(first.Bytes())
		return werr
	}
	if !strings.Contains(errBuf.String(), "already locked") && !strings.Contains(err.Error(), "already locked") {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"restic reports the repository already locked (dest %s / %s) — clearing stale locks with `restic unlock` and retrying once (JAB-392)\n",
		d.ID, d.Name)
	unlockArgs := append(resticRepoArgs(d), "unlock")
	if uerr := retentionExec(ctx, env, cmd.OutOrStdout(), cmd.ErrOrStderr(), "restic", unlockArgs...); uerr != nil {
		return fmt.Errorf("repository was locked and `restic unlock` failed: %w (original error: %v)", uerr, err)
	}
	return retentionExec(ctx, env, stdout, cmd.ErrOrStderr(), "restic", args...)
}

// publishBackupRetentionFailure fires the JAB-392 admin alert (backup.retention.fail)
// so a wedged sweep is visible instead of silently bloating the offsite repo.
// Best-effort: if Redis is down the sweep's non-zero exit is still the truth signal.
func publishBackupRetentionFailure(ctx context.Context, cmd *cobra.Command, failures []string) {
	if requireRedis(cmd, nil) != nil || sharedRedis == nil {
		return
	}
	body := fmt.Sprintf(
		"The nightly backup retention sweep failed for %d (schedule, destination) pair(s); snapshots accumulate unpruned until fixed. A stale restic repository lock is the usual cause — `restic unlock` on the destination clears it. Details:\n  %s",
		len(failures), strings.Join(failures, "\n  "))
	if _, err := notifications.NewQueue(sharedRedis).Publish(ctx, notifications.Envelope{
		EventKind: "backup.retention.fail",
		Severity:  "error",
		Title:     "Backup retention sweep failed",
		Body:      body,
		Deeplink:  "/jabali-admin/backups",
	}); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to publish backup.retention.fail notification: %v\n", err)
	}
}

// retentionJobStore is the slice of the backup_jobs repository the sweep
// uses: a dropped job's row is read to spare a queued or running job, and
// deleted once its snapshots are forgotten.
type retentionJobStore interface {
	Get(ctx context.Context, id string) (*models.BackupJob, error)
	Delete(ctx context.Context, id string) error
}

// forgetBatchSize bounds the snapshot IDs passed to one `restic forget`.
const forgetBatchSize = 50

// forgetForSchedule applies schedule s's keep policy to destination d per
// whole backup job (see backup_retention_jobs.go).
func forgetForSchedule(ctx context.Context, cmd *cobra.Command, s models.BackupSchedule, d *models.BackupDestination, jobs retentionJobStore, dryRun bool) error {
	policy := retentionPolicy{}
	if s.KeepDaily != nil {
		policy.Daily = *s.KeepDaily
	}
	if s.KeepWeekly != nil {
		policy.Weekly = *s.KeepWeekly
	}
	if s.KeepMonthly != nil {
		policy.Monthly = *s.KeepMonthly
	}
	out := cmd.OutOrStdout()
	if policy.empty() {
		fmt.Fprintf(out, "schedule %s dest %s (%s): no positive keep count, skipping\n", s.ID, d.ID, d.Name)
		return nil
	}

	listArgs := append(resticRepoArgs(d), "snapshots", "--json", "--tag", "schedule-id="+s.ID)
	raw, err := runResticCapture(ctx, cmd, d, listArgs)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	var snaps []resticSnapshot
	if err := json.Unmarshal(bytes.TrimSpace(raw), &snaps); err != nil {
		return fmt.Errorf("parse restic snapshots: %w", err)
	}

	plan := planJobRetention(snaps, policy)
	// A job still queued or running is never forgotten, whatever the plan says
	// (its manifest is not written yet, or more stages are coming). A row that
	// cannot be read keeps the job too: retention only deletes what it is sure
	// about.
	var drop []retentionJob
	for _, j := range plan.Drop {
		row, gerr := jobs.Get(ctx, j.JobID)
		switch {
		case errors.Is(gerr, repository.ErrNotFound):
			drop = append(drop, j)
		case gerr != nil:
			fmt.Fprintf(cmd.ErrOrStderr(), "schedule %s dest %s: job %s: read backup_jobs row failed, keeping it: %v\n", s.ID, d.ID, j.JobID, gerr)
		case row.Status == models.BackupJobStatusQueued || row.Status == models.BackupJobStatusRunning:
			fmt.Fprintf(out, "schedule %s dest %s: job %s is %s, keeping it\n", s.ID, d.ID, j.JobID, row.Status)
		default:
			drop = append(drop, j)
		}
	}

	nSnaps := 0
	for _, j := range drop {
		nSnaps += len(j.SnapshotIDs)
	}
	fmt.Fprintf(out,
		"schedule %s dest %s (%s): keep daily=%s weekly=%s monthly=%s per whole backup: %d snapshot(s), keeping %d job(s), forgetting %d job(s) (%d snapshot(s))\n",
		s.ID, d.ID, d.Name, intPtrStr(s.KeepDaily), intPtrStr(s.KeepWeekly), intPtrStr(s.KeepMonthly),
		len(snaps), len(plan.Keep)+len(plan.Drop)-len(drop), len(drop), nSnaps)
	if plan.Untagged > 0 {
		fmt.Fprintf(out, "schedule %s dest %s: %d snapshot(s) carry no job-id tag; left alone\n", s.ID, d.ID, plan.Untagged)
	}
	if len(plan.Ambiguous) > 0 {
		fmt.Fprintf(out, "schedule %s dest %s: job(s) %s span more than one account or host; left alone\n", s.ID, d.ID, strings.Join(plan.Ambiguous, ", "))
	}
	if dryRun {
		for _, j := range drop {
			fmt.Fprintf(out, "[dry-run] would forget job %s (%s, %s, %d snapshot(s))\n",
				j.JobID, j.Series, j.Time.UTC().Format(time.RFC3339), len(j.SnapshotIDs))
		}
		return nil
	}

	// Forget in batches of whole jobs, so a failure part-way leaves every job
	// either fully forgotten or untouched.
	for start := 0; start < len(drop); {
		end, ids := start, []string{}
		for end < len(drop) && (len(ids) == 0 || len(ids)+len(drop[end].SnapshotIDs) <= forgetBatchSize) {
			ids = append(ids, drop[end].SnapshotIDs...)
			end++
		}
		args := append(append(resticRepoArgs(d), "forget"), ids...)
		if err := runResticWithLockRecovery(ctx, cmd, d, args); err != nil {
			return fmt.Errorf("forget %d snapshot(s) of %d job(s): %w", len(ids), end-start, err)
		}
		for _, j := range drop[start:end] {
			deleteForgottenJobRow(ctx, cmd, jobs, j.JobID, d.ID)
		}
		start = end
	}
	return nil
}

// deleteForgottenJobRow removes the backup_jobs row of a job whose snapshots
// were just forgotten from destination destID, so the panel stops listing a
// backup that no longer exists and it stops counting toward a plan's backup
// limit. Only a finished backup job that wrote to that destination is
// deleted; anything else is left as is.
func deleteForgottenJobRow(ctx context.Context, cmd *cobra.Command, jobs retentionJobStore, jobID, destID string) {
	row, err := jobs.Get(ctx, jobID)
	if err != nil || row == nil {
		return
	}
	if row.Kind != models.BackupJobKindAccountBackup && row.Kind != models.BackupJobKindSystemBackup {
		return
	}
	if row.DestinationID == nil || *row.DestinationID != destID {
		return
	}
	if row.Status == models.BackupJobStatusQueued || row.Status == models.BackupJobStatusRunning {
		return
	}
	if err := jobs.Delete(ctx, jobID); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "job %s: snapshots forgotten but deleting its backup_jobs row failed: %v\n", jobID, err)
	}
}

func pruneOneDestination(ctx context.Context, cmd *cobra.Command, d *models.BackupDestination, dryRun bool) error {
	args := []string{
		"--repo", d.URL,
		"--password-file", resticPasswordFile,
	}
	for _, opt := range backupwrapperhelpers.ResticOptionsFor(d) {
		if opt == "" {
			continue
		}
		args = append(args, "-o", opt)
	}
	args = append(args, "prune")
	if dryRun {
		args = append(args, "--dry-run")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "running: restic prune (dest %s / %s)\n", d.ID, d.Name)
	return runResticWithLockRecovery(ctx, cmd, d, args)
}

func destEnv(d *models.BackupDestination) []string {
	if d.CredentialsRef == nil || *d.CredentialsRef == "" {
		return nil
	}
	env, err := internalbackup.LoadEnvFile(*d.CredentialsRef)
	if err != nil {
		// Surface the failure to stderr so SFTP/S3 destinations don't
		// silently fall through to "sftp.command failed" / "missing
		// credentials" inside restic. Common cause: this CLI invoked
		// as a non-root user (creds file is 0600 root:root). The
		// jabali-backup-retention.timer runs as root by design.
		fmt.Fprintf(os.Stderr,
			"WARNING: failed to read credentials env %s for dest %s (%s): %v\n"+
				"  This usually means the CLI is running as a non-root user. The retention\n"+
				"  timer (jabali-backup-retention.timer) runs as root by design — invoke\n"+
				"  this command via sudo, or wait for the timer's daily 04:30 run.\n",
			*d.CredentialsRef, d.ID, d.Name, err)
		return nil
	}
	return env
}

func intPtrStr(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

func assertResticEnvironment() error {
	if _, err := exec.LookPath("restic"); err != nil {
		return fmt.Errorf("restic not on PATH: %w (run install_backup_foundation in install.sh)", err)
	}
	pwFI, err := os.Stat(resticPasswordFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", resticPasswordFile, err)
	}
	if pwFI.Size() == 0 {
		return fmt.Errorf("%s is empty (regenerate via install_backup_foundation)", resticPasswordFile)
	}
	return nil
}

var _ = errors.Is
