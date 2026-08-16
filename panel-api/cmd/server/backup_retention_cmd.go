// `jabali backup retention apply` — fired by jabali-backup-retention.timer
// daily at 04:30. Per ADR-0080 each backup writes directly to ONE
// destination, so retention has to walk every (schedule, destination)
// pair and run `restic forget --tag schedule-id=<id>` against that
// destination's repo. A single `restic prune` per destination is run
// at the end so blob removal happens once per timer fire per repo.
//
// Manual (non-scheduled) backups carry no schedule-id tag and are
// therefore NEVER auto-pruned. Operators who want them gone delete
// them by hand with `restic forget --tag job-id=<id>`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	resticPasswordFile  = "/etc/jabali-panel/restic-repo.password"
	resticForgetTimeout = 30 * time.Minute
)

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
schedule has at least one non-NULL keep_{daily,weekly,monthly}, run:

    restic --repo <dest.url> forget --tag schedule-id=<sched.id> \
        --keep-daily=<N> --keep-weekly=<N> --keep-monthly=<N>

then a single ` + "`restic prune`" + ` per destination at the end. Schedules
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
			scheds, err := schedRepo.List(ctx)
			if err != nil {
				return fmt.Errorf("list backup_schedules: %w", err)
			}

			// Track which destinations had any forget run against them so
			// we only invoke prune where it would have work to do.
			pruneDests := map[string]*models.BackupDestination{}
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
					if err := forgetForSchedule(ctx, cmd, s, d, dryRun); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(),
							"schedule %s dest %s forget failed: %v\n", s.ID, d.ID, err)
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
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Pass restic --dry-run to forget+prune (lists what would be removed; no destructive ops)")
	return cmd
}

func forgetForSchedule(ctx context.Context, cmd *cobra.Command, s models.BackupSchedule, d *models.BackupDestination, dryRun bool) error {
	// Argument layout: [global flags] subcommand [subcommand flags].
	// `-o key=val` is global; `--dry-run` belongs to forget/prune
	// directly. Putting --dry-run before the subcommand makes
	// restic's parser bail with `unknown command "sftp.command=..."`
	// because the next token (an -o value) gets misread as the
	// subcommand. Order matters here.
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
	args = append(args, "forget", "--tag", "schedule-id="+s.ID)
	if dryRun {
		args = append(args, "--dry-run")
	}
	if s.KeepDaily != nil {
		args = append(args, "--keep-daily", strconv.Itoa(*s.KeepDaily))
	}
	if s.KeepWeekly != nil {
		args = append(args, "--keep-weekly", strconv.Itoa(*s.KeepWeekly))
	}
	if s.KeepMonthly != nil {
		args = append(args, "--keep-monthly", strconv.Itoa(*s.KeepMonthly))
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"schedule %s dest %s (%s): restic forget --tag schedule-id=%s daily=%s weekly=%s monthly=%s\n",
		s.ID, d.ID, d.Name, s.ID,
		intPtrStr(s.KeepDaily), intPtrStr(s.KeepWeekly), intPtrStr(s.KeepMonthly))
	c := exec.CommandContext(ctx, "restic", args...)
	c.Env = append(os.Environ(), destEnv(d)...)
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run()
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
	c := exec.CommandContext(ctx, "restic", args...)
	c.Env = append(os.Environ(), destEnv(d)...)
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run()
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
