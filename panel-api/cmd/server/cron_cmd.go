package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/cronops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func cronJobRepoFromDB() repository.CronJobRepository {
	return repository.NewCronJobRepository(sharedDB)
}

// cronopsCLIDeps wires the shared Cron Job Intake (ADR-0083/0101)
// for the CLI: same module the REST handler uses.
func cronopsCLIDeps() cronops.Deps {
	return cronops.Deps{
		Users:    userRepo(),
		Domains:  repository.NewDomainRepository(sharedDB),
		CronJobs: cronJobRepoFromDB(),
		Agent:    sharedAgent,
	}
}

func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage user cron jobs (systemd-user timers)",
	}
	cmd.AddCommand(
		newCronListCmd(),
		newCronAddCmd(),
		newCronUpdateCmd(),
		newCronDeleteCmd(),
		newCronRunNowCmd(),
		newCronHTTPTriggerCmd(),
	)
	return cmd
}

func newCronListCmd() *cobra.Command {
	var userLookup string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List cron jobs (filtered by user, or all)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := cronJobRepoFromDB()
			var rows []*models.CronJob
			var err error
			if userLookup == "" {
				rows, err = repo.ListAll(ctx)
			} else {
				u, uerr := resolveUser(ctx, userLookup)
				if uerr != nil {
					return uerr
				}
				rows, err = repo.ListByUserID(ctx, u.ID)
			}
			if err != nil {
				return fmt.Errorf("list cron jobs: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"jobs": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("No cron jobs.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tUSER_ID\tNAME\tSCHEDULE\tENABLED\tLAST_RUN\tLAST_EXIT")
			for _, j := range rows {
				last := "-"
				if j.LastRunAt != nil {
					last = j.LastRunAt.Format(time.RFC3339)
				}
				ec := "-"
				if j.LastExitCode != nil {
					ec = fmt.Sprintf("%d", *j.LastExitCode)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					j.ID, j.UserID, j.Name, j.Schedule, boolYN(j.Enabled), last, ec)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "filter by user (id|email|username); empty = all")
	return cmd
}

func newCronAddCmd() *cobra.Command {
	var (
		userLookup string
		name       string
		schedule   string
		command    string
		disabled   bool
	)
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add a cron job (5-field cron, allowlisted commands only)",
		Long:    "Schedule format is standard 5-field cron (e.g. '*/15 * * * *'). Command must pass cron-validate (php/wp/curl/git etc. allowlist; absolute paths only).",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, userLookup)
			if err != nil {
				return err
			}
			// Thin adapter over cronops — same Cron Job Intake the
			// REST API uses. ADR-0101: applied synchronously.
			job, err := cronops.Create(ctx, cronopsCLIDeps(), cronops.CreateInput{
				UserID:   u.ID,
				Name:     name,
				Command:  command,
				Schedule: schedule,
				Enabled:  !disabled,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(job)
			}
			cliAuditOK(ctx, "cron.create", "cron_job", job.ID, nil)
			fmt.Printf("Created cron job %s (%s) for %s (applied).\n",
				job.ID, job.Name, derefStr(u.Username))
			return nil
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "user (id|email|username) (required)")
	cmd.Flags().StringVar(&name, "name", "", "job name (required)")
	cmd.Flags().StringVar(&schedule, "schedule", "", "5-field cron expression e.g. '*/15 * * * *' (required)")
	cmd.Flags().StringVar(&command, "command", "", "command to run (required, allowlisted)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "create disabled")
	_ = cmd.MarkFlagRequired("user")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("schedule")
	_ = cmd.MarkFlagRequired("command")
	return cmd
}

func newCronUpdateCmd() *cobra.Command {
	var (
		name     string
		schedule string
		command  string
		enable   bool
		disable  bool
	)
	cmd := &cobra.Command{
		Use:     "update <job-id>",
		Short:   "Update a cron job",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := cronJobRepoFromDB()
			job, err := repo.FindByID(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("cron job %q not found", args[0])
				}
				return fmt.Errorf("find: %w", err)
			}
			var patch cronops.UpdatePatch
			changed := false
			if cmd.Flags().Changed("name") {
				patch.Name = &name
				changed = true
			}
			if cmd.Flags().Changed("schedule") {
				patch.Schedule = &schedule
				changed = true
			}
			if cmd.Flags().Changed("command") {
				patch.Command = &command
				changed = true
			}
			if enable {
				v := true
				patch.Enabled = &v
				changed = true
			}
			if disable {
				v := false
				patch.Enabled = &v
				changed = true
			}
			if !changed {
				return fmt.Errorf("no changes specified")
			}
			// Delegate validate+persist+apply to cronops (the find
			// above stays for the not-found UX + change detection).
			updated, err := cronops.Update(ctx, cronopsCLIDeps(), job.ID, patch)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(updated)
			}
			cliAuditOK(ctx, "cron.update", "cron_job", updated.ID, nil)
			fmt.Printf("Updated cron job %s (applied).\n", updated.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "")
	cmd.Flags().StringVar(&schedule, "schedule", "", "")
	cmd.Flags().StringVar(&command, "command", "", "")
	cmd.Flags().BoolVar(&enable, "enable", false, "mark job enabled")
	cmd.Flags().BoolVar(&disable, "disable", false, "mark job disabled")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func newCronDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <job-id>",
		Short:   "Delete a cron job (removes the systemd timer, then the row)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 40*time.Second)
			defer cancel()
			repo := cronJobRepoFromDB()
			job, err := repo.FindByID(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("cron job %q not found", args[0])
				}
				return fmt.Errorf("find: %w", err)
			}
			if !force {
				fmt.Printf("Delete cron job %s (%s, %s)? [y/N]: ", job.ID, job.Name, job.Schedule)
				var c string
				fmt.Scanln(&c)
				if c != "y" && c != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			// JAB-293: shared safe delete — synchronously removes the host timer
			// and drops the row only on success; a failed remove KEEPS the row so
			// the reconciler still has a handle (deleting a last job's row on a
			// failed remove would leave a live timer forever).
			if err := cronops.Delete(ctx, cronopsCLIDeps(), job.ID); err != nil {
				if errors.Is(err, cronops.ErrAgentFailed) {
					return fmt.Errorf("host timer removal failed; the job row was kept for retry: %w", err)
				}
				return fmt.Errorf("delete cron job: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]string{"deleted": job.ID})
			}
			cliAuditOK(ctx, "cron.delete", "cron_job", job.ID, nil)
			fmt.Printf("Deleted cron job %s.\n", job.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func newCronRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-now <job-id>",
		Short: "Run a cron job immediately via the agent (synchronous)",
		Long: `Runs the job's command immediately as the target user via the agent
(synchronous), returning its real exit code plus stdout/stderr.

The agent executes it with ` + "`runuser`" + ` -- a plain setuid/setgid drop --
so run-now does NOT depend on the system D-Bus bus, the user's
systemd-user manager, or linger being up (GH #296). It works from any
non-interactive context (cron, CI, headless ssh) regardless of host
shape. (The scheduled timer run still fires inside the user manager;
this one-shot does not need the slice.)`,
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			repo := cronJobRepoFromDB()
			job, err := repo.FindByID(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("cron job %q not found", args[0])
				}
				return fmt.Errorf("find: %w", err)
			}
			u, err := userRepo().FindByID(ctx, job.UserID)
			if err != nil {
				return fmt.Errorf("lookup user: %w", err)
			}
			// cron.run_now requires the command + the user's owned
			// docroots (the agent re-validates before exec). The CLI
			// previously sent neither, so post-#273 the agent rejected
			// every `jabali cron run-now <id>` with "command required".
			var docroots []string
			if owned, _, dErr := repository.NewDomainRepository(sharedDB).ListByUserID(ctx, job.UserID, repository.ListOptions{Limit: 1000}); dErr == nil {
				for _, dm := range owned {
					if dm.DocRoot != "" {
						docroots = append(docroots, dm.DocRoot)
					}
				}
			}
			raw, err := sharedAgent.Call(ctx, "cron.run_now", map[string]any{
				"user_id":        u.ID,
				"username":       derefStr(u.Username),
				"job_id":         job.ID,
				"command":        job.Command,
				"owned_docroots": docroots,
			})
			if err != nil {
				return fmt.Errorf("cron.run_now: %w", err)
			}
			var resp struct {
				ExitCode int    `json:"exit_code"`
				Stdout   string `json:"stdout,omitempty"`
				Stderr   string `json:"stderr,omitempty"`
			}
			_ = json.Unmarshal(raw, &resp)
			if jsonOutput {
				return printJSON(resp)
			}
			fmt.Printf("Exit: %d\n", resp.ExitCode)
			if resp.Stdout != "" {
				fmt.Println("--- stdout ---")
				fmt.Println(resp.Stdout)
			}
			if resp.Stderr != "" {
				fmt.Println("--- stderr ---")
				fmt.Println(resp.Stderr)
			}
			return nil
		},
	}
}
