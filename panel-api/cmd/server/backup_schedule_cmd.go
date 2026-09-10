package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupscheduleops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func backupScheduleRepoFromDB() repository.BackupScheduleRepository {
	return repository.NewBackupScheduleRepository(sharedDB)
}

func newBackupScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "schedule",
		Aliases: []string{"sched"},
		Short:   "Manage backup schedules",
	}
	cmd.AddCommand(
		newBackupScheduleListCmd(),
		newBackupScheduleGetCmd(),
		newBackupScheduleCreateCmd(),
		newBackupScheduleUpdateCmd(),
		newBackupScheduleDeleteCmd(),
		newBackupScheduleRunNowCmd(),
	)
	return cmd
}

func newBackupScheduleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List backup schedules",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			rows, err := backupScheduleRepoFromDB().List(ctx)
			if err != nil {
				return fmt.Errorf("list schedules: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"schedules": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("No schedules.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tKIND\tCRON\tENABLED\tNEXT_RUN\tLAST_RUN")
			for _, s := range rows {
				next := "-"
				if s.NextRunAt != nil {
					next = s.NextRunAt.Format(time.RFC3339)
				}
				last := "-"
				if s.LastRunAt != nil {
					last = s.LastRunAt.Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Kind, s.CronExpr, boolYN(s.Enabled), next, last)
			}
			return w.Flush()
		},
	}
}

func newBackupScheduleGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show a backup schedule with destinations + users",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := backupScheduleRepoFromDB()
			s, err := repo.GetWithDestinations(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("schedule %q not found", args[0])
				}
				return fmt.Errorf("get schedule: %w", err)
			}
			users, err := repo.GetUserIDs(ctx, s.ID)
			if err != nil {
				return fmt.Errorf("get users: %w", err)
			}
			s.UserIDs = users
			if jsonOutput {
				return printJSON(s)
			}
			fmt.Printf("ID:           %s\n", s.ID)
			fmt.Printf("Kind:         %s\n", s.Kind)
			fmt.Printf("Cron:         %s\n", s.CronExpr)
			fmt.Printf("Enabled:      %s\n", boolYN(s.Enabled))
			if s.NextRunAt != nil {
				fmt.Printf("Next run:     %s\n", s.NextRunAt.Format(time.RFC3339))
			}
			if s.LastRunAt != nil {
				fmt.Printf("Last run:     %s\n", s.LastRunAt.Format(time.RFC3339))
			}
			fmt.Printf("Destinations: %d\n", len(s.Destinations))
			for _, d := range s.Destinations {
				fmt.Printf("  - %s (%s)\n", d.Name, d.ID)
			}
			fmt.Printf("Users:        %d\n", len(s.UserIDs))
			for _, u := range s.UserIDs {
				fmt.Printf("  - %s\n", u)
			}
			if s.Kind == models.BackupScheduleKindAccount {
				fmt.Printf("Include sys:  %s\n", boolYN(s.IncludeSystemBackup))
			}
			if s.KeepDaily != nil {
				fmt.Printf("Keep daily:   %d\n", *s.KeepDaily)
			}
			if s.KeepWeekly != nil {
				fmt.Printf("Keep weekly:  %d\n", *s.KeepWeekly)
			}
			if s.KeepMonthly != nil {
				fmt.Printf("Keep monthly: %d\n", *s.KeepMonthly)
			}
			return nil
		},
	}
}

func newBackupScheduleCreateCmd() *cobra.Command {
	var (
		kind          string
		cronExpr      string
		preset        string
		disabled      bool
		destinations  []string
		users         []string
		includeSystem bool
		keepDaily     int
		keepWeekly    int
		keepMonthly   int
	)
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a backup schedule",
		Long:    "Create a backup schedule. Use --preset daily|weekly|monthly OR --cron '0 3 * * *'. Multiple --destination flags resolve names or IDs. For account_backup, multiple --user (id|email|username) restrict fan-out.",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if kind != models.BackupScheduleKindAccount && kind != models.BackupScheduleKindSystem {
				return fmt.Errorf("invalid --kind %q (allowed: account_backup, system_backup)", kind)
			}
			if preset != "" {
				p, ok := internalbackup.PresetCronExpr[preset]
				if !ok {
					return fmt.Errorf("invalid --preset %q (allowed: daily, weekly, monthly)", preset)
				}
				cronExpr = p
			}
			if cronExpr == "" {
				return fmt.Errorf("either --cron or --preset is required")
			}
			destIDs, err := resolveDestinationIDs(ctx, destinations)
			if err != nil {
				return err
			}
			userIDs, err := resolveUserIDs(ctx, users)
			if err != nil {
				return err
			}
			enabled := !disabled
			in := backupscheduleops.CreateInput{
				Kind:                kind,
				UserIDs:             userIDs,
				IncludeSystemBackup: includeSystem,
				CronExpr:            cronExpr,
				Enabled:             &enabled,
				DestinationIDs:      destIDs,
			}
			if cmd.Flags().Changed("keep-daily") {
				in.KeepDaily = &keepDaily
			}
			if cmd.Flags().Changed("keep-weekly") {
				in.KeepWeekly = &keepWeekly
			}
			if cmd.Flags().Changed("keep-monthly") {
				in.KeepMonthly = &keepMonthly
			}
			// One shared lifecycle: admin-target rejection, system-kind
			// normalization, cron validation, and the transactional
			// row+memberships commit all live in the leaf now (JAB-307), so
			// the CLI can no longer schedule an admin account or leave a
			// half-written schedule when a join insert fails.
			s, err := backupscheduleops.Create(ctx, backupscheduleops.Deps{
				Schedules: backupScheduleRepoFromDB(),
				Users:     repository.NewUserRepository(sharedDB),
			}, in)
			if err != nil {
				return mapScheduleOpErr(err)
			}
			if jsonOutput {
				return printJSON(s)
			}
			cliAuditOK(ctx, "backup_schedule.create", "backup_schedule", s.ID, nil)
			next := ""
			if s.NextRunAt != nil {
				next = s.NextRunAt.Format(time.RFC3339)
			}
			fmt.Printf("Created schedule %s (%s, cron=%s, next=%s)\n",
				s.ID, s.Kind, s.CronExpr, next)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "schedule kind: account_backup|system_backup (required)")
	cmd.Flags().StringVar(&cronExpr, "cron", "", "5-field cron expression (e.g. '0 3 * * *')")
	cmd.Flags().StringVar(&preset, "preset", "", "preset: daily|weekly|monthly (mutually exclusive with --cron)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "create in disabled state")
	cmd.Flags().StringArrayVar(&destinations, "destination", nil, "destination id or name (repeatable)")
	cmd.Flags().StringArrayVar(&users, "user", nil, "user id|email|username for account_backup fan-out (repeatable; empty=all non-admins)")
	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "for account_backup: also fire system_backup each tick")
	cmd.Flags().IntVar(&keepDaily, "keep-daily", 0, "restic forget --keep-daily")
	cmd.Flags().IntVar(&keepWeekly, "keep-weekly", 0, "restic forget --keep-weekly")
	cmd.Flags().IntVar(&keepMonthly, "keep-monthly", 0, "restic forget --keep-monthly")
	cmd.MarkFlagsMutuallyExclusive("cron", "preset")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func newBackupScheduleUpdateCmd() *cobra.Command {
	var (
		cronExpr      string
		preset        string
		enable        bool
		disable       bool
		includeSystem string
		keepDaily     int
		keepWeekly    int
		keepMonthly   int
		destinations  []string
		users         []string
		clearDests    bool
		clearUsers    bool
	)
	cmd := &cobra.Command{
		Use:     "update <id>",
		Short:   "Update a backup schedule",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			if preset != "" {
				p, ok := internalbackup.PresetCronExpr[preset]
				if !ok {
					return fmt.Errorf("invalid --preset %q", preset)
				}
				cronExpr = p
			}

			in := backupscheduleops.UpdateInput{ID: args[0]}
			if cronExpr != "" {
				in.CronExpr = &cronExpr
			}
			if enable {
				v := true
				in.Enabled = &v
			}
			if disable {
				v := false
				in.Enabled = &v
			}
			switch includeSystem {
			case "true":
				v := true
				in.IncludeSystemBackup = &v
			case "false":
				v := false
				in.IncludeSystemBackup = &v
			case "":
			default:
				return fmt.Errorf("invalid --include-system %q (want true or false)", includeSystem)
			}
			if cmd.Flags().Changed("keep-daily") {
				in.KeepDaily = &keepDaily
			}
			if cmd.Flags().Changed("keep-weekly") {
				in.KeepWeekly = &keepWeekly
			}
			if cmd.Flags().Changed("keep-monthly") {
				in.KeepMonthly = &keepMonthly
			}
			if clearDests {
				in.DestinationIDs = &[]string{}
			} else if len(destinations) > 0 {
				dstIDs, err := resolveDestinationIDs(ctx, destinations)
				if err != nil {
					return err
				}
				in.DestinationIDs = &dstIDs
			}
			if clearUsers {
				in.UserIDs = &[]string{}
			} else if len(users) > 0 {
				uIDs, err := resolveUserIDs(ctx, users)
				if err != nil {
					return err
				}
				in.UserIDs = &uIDs
			}

			if in.CronExpr == nil && in.Enabled == nil && in.IncludeSystemBackup == nil &&
				in.KeepDaily == nil && in.KeepWeekly == nil && in.KeepMonthly == nil &&
				in.DestinationIDs == nil && in.UserIDs == nil {
				return fmt.Errorf("no changes specified")
			}

			s, err := backupscheduleops.Update(ctx, backupscheduleops.Deps{
				Schedules: backupScheduleRepoFromDB(),
				Users:     repository.NewUserRepository(sharedDB),
			}, in)
			if err != nil {
				var ure *backupscheduleops.UserRejectedError
				switch {
				case errors.Is(err, repository.ErrNotFound):
					return fmt.Errorf("schedule %q not found", args[0])
				case errors.As(err, &ure) && errors.Is(err, backupscheduleops.ErrAdminUser):
					return fmt.Errorf("user %s is an admin account and cannot be a backup-schedule target", ure.UserID)
				case errors.As(err, &ure):
					return fmt.Errorf("user %s not found", ure.UserID)
				case errors.Is(err, backupscheduleops.ErrInvalidCron):
					return err
				default:
					return fmt.Errorf("update schedule: %w", err)
				}
			}
			if jsonOutput {
				return printJSON(s)
			}
			cliAuditOK(ctx, "backup_schedule.update", "backup_schedule", s.ID, nil)
			fmt.Printf("Updated schedule %s\n", s.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&cronExpr, "cron", "", "new cron expression")
	cmd.Flags().StringVar(&preset, "preset", "", "preset: daily|weekly|monthly")
	cmd.Flags().BoolVar(&enable, "enable", false, "mark schedule enabled")
	cmd.Flags().BoolVar(&disable, "disable", false, "mark schedule disabled")
	cmd.Flags().StringVar(&includeSystem, "include-system", "", "true|false (account_backup only)")
	cmd.Flags().IntVar(&keepDaily, "keep-daily", 0, "")
	cmd.Flags().IntVar(&keepWeekly, "keep-weekly", 0, "")
	cmd.Flags().IntVar(&keepMonthly, "keep-monthly", 0, "")
	cmd.Flags().StringArrayVar(&destinations, "destination", nil, "replace destinations (repeatable)")
	cmd.Flags().StringArrayVar(&users, "user", nil, "replace users (repeatable)")
	cmd.Flags().BoolVar(&clearDests, "clear-destinations", false, "remove all destinations")
	cmd.Flags().BoolVar(&clearUsers, "clear-users", false, "remove all users (= fan-out to all)")
	cmd.MarkFlagsMutuallyExclusive("cron", "preset")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive("destination", "clear-destinations")
	cmd.MarkFlagsMutuallyExclusive("user", "clear-users")
	return cmd
}

func newBackupScheduleDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete a backup schedule",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := backupScheduleRepoFromDB()
			s, err := repo.Get(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("schedule %q not found", args[0])
				}
				return fmt.Errorf("get: %w", err)
			}
			if !force {
				fmt.Printf("Delete schedule %s (%s, cron=%s)? [y/N]: ", s.ID, s.Kind, s.CronExpr)
				var c string
				fmt.Scanln(&c)
				if c != "y" && c != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			if err := repo.Delete(ctx, s.ID); err != nil {
				return fmt.Errorf("delete schedule: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]string{"deleted": s.ID})
			}
			cliAuditOK(ctx, "backup_schedule.delete", "backup_schedule", s.ID, nil)
			fmt.Printf("Deleted schedule %s\n", s.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func newBackupScheduleRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "run-now <id>",
		Short:   "Trigger a schedule by advancing next_run_at to now (scheduler picks up within ≤60s)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := backupScheduleRepoFromDB()
			s, err := repo.Get(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("schedule %q not found", args[0])
				}
				return fmt.Errorf("get: %w", err)
			}
			if !s.Enabled {
				return fmt.Errorf("schedule %s is disabled — enable it before run-now", s.ID)
			}
			now := time.Now().UTC()
			if err := repo.UpdateNextRun(ctx, s.ID, now); err != nil {
				return fmt.Errorf("advance next_run_at: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"id":          s.ID,
					"next_run_at": now,
					"detail":      "scheduler tick will fire within next interval",
				})
			}
			fmt.Printf("Scheduled %s for immediate run; scheduler tick will fire within ≤60s.\n", s.ID)
			return nil
		},
	}
}

func resolveDestinationIDs(ctx context.Context, items []string) ([]string, error) {
	out := make([]string, 0, len(items))
	for _, it := range items {
		d, err := resolveBackupDestination(ctx, it)
		if err != nil {
			return nil, err
		}
		out = append(out, d.ID)
	}
	return out, nil
}

func resolveUserIDs(ctx context.Context, items []string) ([]string, error) {
	out := make([]string, 0, len(items))
	for _, it := range items {
		u, err := resolveUser(ctx, it)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", it, err)
		}
		out = append(out, u.ID)
	}
	return out, nil
}

// mapScheduleOpErr turns a backupscheduleops error into a caller-friendly CLI
// error. Every path returns a non-nil error so the command exits non-zero
// (a silently-accepted admin target would be feedback_silent_exit0_failures).
func mapScheduleOpErr(err error) error {
	var ure *backupscheduleops.UserRejectedError
	if errors.As(err, &ure) {
		if errors.Is(err, backupscheduleops.ErrAdminUser) {
			return fmt.Errorf("user %s is an admin account and cannot be a backup-schedule target", ure.UserID)
		}
		return fmt.Errorf("user %s not found", ure.UserID)
	}
	return err
}
