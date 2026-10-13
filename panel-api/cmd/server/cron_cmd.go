package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
			owners, err := userRepo().FindByIDs(ctx, cronJobOwnerIDs(rows))
			if err != nil {
				return fmt.Errorf("look up cron job owners: %w", err)
			}
			return renderCronJobs(os.Stdout, rows, cronOwnerNames(owners))
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "filter by user (id|email|username); empty = all")
	return cmd
}

// cronJobOwnerIDs returns the distinct owners of rows.
func cronJobOwnerIDs(rows []*models.CronJob) []string {
	seen := make(map[string]bool, len(rows))
	ids := make([]string, 0, len(rows))
	for _, j := range rows {
		if !seen[j.UserID] {
			seen[j.UserID] = true
			ids = append(ids, j.UserID)
		}
	}
	return ids
}

// cronOwnerNames maps each user id to the name the cron table shows for it:
// the account's username, or the email of an admin, who has none.
func cronOwnerNames(users []models.User) map[string]string {
	names := make(map[string]string, len(users))
	for _, u := range users {
		if u.Username != nil && *u.Username != "" {
			names[u.ID] = *u.Username
		} else {
			names[u.ID] = u.Email
		}
	}
	return names
}

// renderCronJobs prints the cron table. The USER column names each job's
// account, so an admin can tell whose jobs are whose (GH #2093); a job whose
// account is gone shows the user id it still carries.
func renderCronJobs(out io.Writer, rows []*models.CronJob, owners map[string]string) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSER\tNAME\tSCHEDULE\tENABLED\tLAST_RUN\tLAST_EXIT")
	for _, j := range rows {
		owner := owners[j.UserID]
		if owner == "" {
			owner = j.UserID
		}
		last := "-"
		if j.LastRunAt != nil {
			last = j.LastRunAt.Format(time.RFC3339)
		}
		ec := "-"
		if j.LastExitCode != nil {
			ec = fmt.Sprintf("%d", *j.LastExitCode)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			j.ID, owner, j.Name, j.Schedule, boolYN(j.Enabled), last, ec)
	}
	return w.Flush()
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
				if err := confirmOrAbort(cmd, os.Stdin, fmt.Sprintf("Delete cron job %s (%s, %s)?", job.ID, job.Name, job.Schedule), "--force"); err != nil {
					return err
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

// cronRunNowCLIWaitSeconds is how long `jabali cron run-now` waits for the run
// before reporting that it is still running (GH #2122). It stays inside the
// command's 5-minute agent call.
const cronRunNowCLIWaitSeconds = 240

func newCronRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-now <job-id>",
		Short: "Run a cron job immediately; a run still going after 4 minutes keeps running",
		Long: `Runs the job once, now, as the target user via the agent, and prints its
exit code and output.

A job whose systemd-user unit is installed runs through that unit, exactly
like a scheduled run. The command waits up to 4 minutes for it; a run still
going then keeps running in the background (GH #2122), its output goes to
the job's log, and Last run is recorded when it ends. A run of the job that
is already under way is not started again (exit 1).

A job without an installed unit yet, or on a host where the user's systemd
manager can't be reached, runs directly with ` + "`runuser`" + ` -- a plain
setuid/setgid drop that needs no D-Bus, user manager or linger (GH #296) --
and is stopped when the command's 5-minute limit ends.`,
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
			// A root job (JAB-293) runs as root through its system unit, and
			// only while its owner is an admin (cronops' invariant: a
			// tenant-owned root row must never have root run its command).
			username := derefStr(u.Username)
			if job.RunAsRoot {
				if !u.IsAdmin {
					return fmt.Errorf("cron job %s is a root job owned by a non-admin account; delete it and recreate it as an admin", job.ID)
				}
				username = "root"
			}
			raw, err := sharedAgent.Call(ctx, "cron.run_now", map[string]any{
				"user_id":        u.ID,
				"username":       username,
				"run_as_root":    job.RunAsRoot,
				"job_id":         job.ID,
				"command":        job.Command,
				"owned_docroots": docroots,
				// GH #2122: wait most of this command's 5 minutes; a run that
				// is still going then carries on in the job's unit.
				"wait_seconds": cronRunNowCLIWaitSeconds,
			})
			if err != nil {
				return fmt.Errorf("cron.run_now: %w", err)
			}
			var resp struct {
				ExitCode       int    `json:"exit_code"`
				Stdout         string `json:"stdout,omitempty"`
				Stderr         string `json:"stderr,omitempty"`
				Output         string `json:"output,omitempty"`
				Running        bool   `json:"running,omitempty"`
				AlreadyRunning bool   `json:"already_running,omitempty"`
			}
			_ = json.Unmarshal(raw, &resp)
			if jsonOutput {
				return printJSON(resp)
			}
			if resp.AlreadyRunning {
				return fmt.Errorf("cron job %s is already running; nothing was started", job.ID)
			}
			if resp.Running {
				fmt.Printf("Still running after %d seconds; it carries on in the background.\n", cronRunNowCLIWaitSeconds)
				fmt.Println("Its output goes to the job's log in the panel. To follow it here:")
				fmt.Printf("  journalctl -f _SYSTEMD_USER_UNIT=jabali-cron-%s.service\n", job.ID)
				return nil
			}
			fmt.Printf("Exit: %d\n", resp.ExitCode)
			if resp.Output != "" {
				fmt.Println("--- output ---")
				fmt.Println(resp.Output)
			}
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
