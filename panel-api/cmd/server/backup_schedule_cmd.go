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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
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
			next, err := internalbackup.NextFire(cronExpr, time.Now().UTC())
			if err != nil {
				return err
			}
			destIDs, err := resolveDestinationIDs(ctx, destinations)
			if err != nil {
				return err
			}
			userIDs, err := resolveUserIDs(ctx, users)
			if err != nil {
				return err
			}
			s := &models.BackupSchedule{
				ID:                  ids.NewULID(),
				Kind:                kind,
				CronExpr:            cronExpr,
				Enabled:             !disabled,
				IncludeSystemBackup: includeSystem,
				NextRunAt:           &next,
			}
			if cmd.Flags().Changed("keep-daily") {
				s.KeepDaily = &keepDaily
			}
			if cmd.Flags().Changed("keep-weekly") {
				s.KeepWeekly = &keepWeekly
			}
			if cmd.Flags().Changed("keep-monthly") {
				s.KeepMonthly = &keepMonthly
			}
			repo := backupScheduleRepoFromDB()
			if err := repo.Create(ctx, s); err != nil {
				return fmt.Errorf("create schedule: %w", err)
			}
			if len(destIDs) > 0 {
				if err := repo.ReplaceDestinations(ctx, s.ID, destIDs); err != nil {
					return fmt.Errorf("link destinations: %w", err)
				}
			}
			if len(userIDs) > 0 {
				if err := repo.ReplaceUsers(ctx, s.ID, userIDs); err != nil {
					return fmt.Errorf("link users: %w", err)
				}
			}
			if jsonOutput {
				return printJSON(s)
			}
			cliAuditOK(ctx, "backup_schedule.create", "backup_schedule", s.ID, nil)
			fmt.Printf("Created schedule %s (%s, cron=%s, next=%s)\n",
				s.ID, s.Kind, s.CronExpr, next.Format(time.RFC3339))
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
			repo := backupScheduleRepoFromDB()
			s, err := repo.Get(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("schedule %q not found", args[0])
				}
				return fmt.Errorf("get: %w", err)
			}
			changed := false
			if preset != "" {
				p, ok := internalbackup.PresetCronExpr[preset]
				if !ok {
					return fmt.Errorf("invalid --preset %q", preset)
				}
				cronExpr = p
			}
			if cronExpr != "" {
				next, err := internalbackup.NextFire(cronExpr, time.Now().UTC())
				if err != nil {
					return err
				}
				s.CronExpr = cronExpr
				s.NextRunAt = &next
				changed = true
			}
			if enable {
				s.Enabled = true
				changed = true
			}
			if disable {
				s.Enabled = false
				changed = true
			}
			if includeSystem == "true" {
				s.IncludeSystemBackup = true
				changed = true
			} else if includeSystem == "false" {
				s.IncludeSystemBackup = false
				changed = true
			}
			if cmd.Flags().Changed("keep-daily") {
				s.KeepDaily = &keepDaily
				changed = true
			}
			if cmd.Flags().Changed("keep-weekly") {
				s.KeepWeekly = &keepWeekly
				changed = true
			}
			if cmd.Flags().Changed("keep-monthly") {
				s.KeepMonthly = &keepMonthly
				changed = true
			}
			if changed {
				if err := repo.Update(ctx, s); err != nil {
					return fmt.Errorf("update schedule: %w", err)
				}
			}
			if clearDests {
				if err := repo.ReplaceDestinations(ctx, s.ID, nil); err != nil {
					return fmt.Errorf("clear destinations: %w", err)
				}
			} else if len(destinations) > 0 {
				dstIDs, err := resolveDestinationIDs(ctx, destinations)
				if err != nil {
					return err
				}
				if err := repo.ReplaceDestinations(ctx, s.ID, dstIDs); err != nil {
					return fmt.Errorf("link destinations: %w", err)
				}
			}
			if clearUsers {
				if err := repo.ReplaceUsers(ctx, s.ID, nil); err != nil {
					return fmt.Errorf("clear users: %w", err)
				}
			} else if len(users) > 0 {
				uIDs, err := resolveUserIDs(ctx, users)
				if err != nil {
					return err
				}
				if err := repo.ReplaceUsers(ctx, s.ID, uIDs); err != nil {
					return fmt.Errorf("link users: %w", err)
				}
			}
			if !changed && !clearDests && !clearUsers && len(destinations) == 0 && len(users) == 0 {
				return fmt.Errorf("no changes specified")
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
