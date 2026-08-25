// Database operations CLI (Gitea #559): per-database backup/restore, admin
// maintenance + process management, and root-password rotation. Each dispatches
// the same agent verb the API does (databases.go / databases_admin_ops.go), with
// the same engine variants (mariadb/postgres). Config tuning (get/put) is
// deferred — it needs the dbtuning allowlist validation + DBAdmin tuning repo and
// is tracked for a follow-up. Mutations are CLI-audited.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dbtuning"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func dbEngineValidCLI(e string) bool { return e == "mariadb" || e == "postgres" }

func resolveDatabaseCLI(ctx context.Context, ref string) (*models.Database, error) {
	repo := dbRepoFromDB()
	if d, err := repo.FindByID(ctx, ref); err == nil && d != nil {
		return d, nil
	}
	rows, _, err := repo.List(ctx, repository.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Name == ref {
			return &rows[i], nil
		}
	}
	return nil, fmt.Errorf("no database with id or name %q", ref)
}

func newDBBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "backup <db-id|db-name>",
		Short:   "Create a backup of a database (returns the dump path)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			d, err := resolveDatabaseCLI(ctx, args[0])
			if err != nil {
				return err
			}
			raw, err := sharedAgent.Call(ctx, "db.backup", map[string]any{"db_name": d.Name})
			if err != nil {
				return fmt.Errorf("agent db.backup: %w", err)
			}
			cliAuditOK(ctx, "database.backup", "database", d.ID, &d.UserID)
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newDBRestoreCmd() *cobra.Command {
	var file string
	var force bool
	cmd := &cobra.Command{
		Use:     "restore <db-id|db-name>",
		Short:   "Restore a database from a .sql dump on the host",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("--file (path to a .sql dump on the host) is required")
			}
			if !force {
				return fmt.Errorf("restore overwrites the database contents; re-run with --force")
			}
			if _, err := os.Stat(file); err != nil {
				return fmt.Errorf("dump file not readable: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			d, err := resolveDatabaseCLI(ctx, args[0])
			if err != nil {
				return err
			}
			if _, err := sharedAgent.Call(ctx, "db.restore", map[string]any{"db_name": d.Name, "path": file}); err != nil {
				return fmt.Errorf("agent db.restore: %w", err)
			}
			cliAuditOK(ctx, "database.restore", "database", d.ID, &d.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Restored %s from %s\n", d.Name, file)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a .sql dump on the host (required)")
	cmd.Flags().BoolVar(&force, "force", false, "confirm the overwrite")
	return cmd
}

func newDBProcessesCmd() *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:     "processes",
		Short:   "List database processes/activity",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dbEngineValidCLI(engine) {
				return fmt.Errorf("--engine must be mariadb|postgres")
			}
			verb := "db.processlist"
			if engine == "postgres" {
				verb = "db.postgres.activity"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			return csCall(ctx, verb, map[string]any{})
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "mariadb|postgres")
	return cmd
}

func newDBKillCmd() *cobra.Command {
	var engine string
	var force bool
	cmd := &cobra.Command{
		Use:     "kill <process-id>",
		Short:   "Kill/terminate a database process",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dbEngineValidCLI(engine) {
				return fmt.Errorf("--engine must be mariadb|postgres")
			}
			if !force {
				return fmt.Errorf("killing a DB process can abort a transaction; re-run with --force")
			}
			verb := "db.kill"
			if engine == "postgres" {
				verb = "db.postgres.terminate"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			// JAB-305: audit exactly one true outcome. This recorded only the
			// success case, so a failed privileged kill left no audit trail;
			// mirror the maintenance + root-password commands (Err on failure,
			// OK on success).
			if err := csCall(ctx, verb, map[string]any{"id": args[0]}); err != nil {
				cliAuditErr(ctx, "database.process_kill", "database_process", args[0], nil)
				return err
			}
			cliAuditOK(ctx, "database.process_kill", "database_process", args[0], nil)
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "mariadb|postgres")
	cmd.Flags().BoolVar(&force, "force", false, "confirm the kill")
	return cmd
}

func newDBMaintenanceCmd() *cobra.Command {
	var engine, scope string
	cmd := &cobra.Command{
		Use:     "maintenance",
		Short:   "Run optimize/analyze (mariadb) or vacuum/analyze (postgres)",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dbEngineValidCLI(engine) {
				return fmt.Errorf("--engine must be mariadb|postgres")
			}
			if scope == "" {
				scope = "all"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			adminRepo := repository.NewDBAdminRepository(sharedDB)
			if running, _ := adminRepo.RunningJob(ctx, engine); running != nil {
				return fmt.Errorf("maintenance already running for %s (job %s)", engine, running.ID)
			}
			job := &models.DBAdminJob{ID: ulid.Make().String(), Engine: engine, Kind: "maintenance", Scope: scope, Status: "running", ActorUserID: "cli"}
			if err := adminRepo.CreateJob(ctx, job); err != nil {
				return fmt.Errorf("create job: %w", err)
			}
			verb := "db.maintenance"
			if engine == "postgres" {
				verb = "db.postgres.maintenance"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Running %s maintenance (scope=%s, job %s) — this may take several minutes…\n", engine, scope, job.ID)
			raw, aerr := sharedAgent.Call(ctx, verb, map[string]any{"scope": scope})
			status, summary := "ok", ""
			if aerr != nil {
				status, summary = "error", aerr.Error()
			} else {
				var r struct {
					Summary string `json:"summary"`
				}
				_ = json.Unmarshal(raw, &r)
				summary = r.Summary
			}
			_ = adminRepo.FinishJob(ctx, job.ID, status, summary)
			// JAB-305: audit exactly one true outcome. This recorded
			// cliAuditOK unconditionally — a false success audit when the Agent
			// call failed. Mirror the kill command: OK only on success, Err on
			// failure.
			if aerr != nil {
				cliAuditErr(ctx, "database.maintenance", "db_admin_job", job.ID, nil)
				return fmt.Errorf("maintenance failed: %w", aerr)
			}
			cliAuditOK(ctx, "database.maintenance", "db_admin_job", job.ID, nil)
			fmt.Fprintf(cmd.OutOrStdout(), "Maintenance %s: %s\n", status, summary)
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "mariadb|postgres")
	cmd.Flags().StringVar(&scope, "scope", "all", "'all' or a database name")
	return cmd
}

func newDBMaintenanceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "maintenance-status <job-id>",
		Short:   "Show a maintenance job's status",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			job, err := repository.NewDBAdminRepository(sharedDB).GetJob(ctx, args[0])
			if err != nil {
				return fmt.Errorf("job not found: %w", err)
			}
			return printJSON(job)
		},
	}
}

func newDBRootPasswordCmd() *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:     "root-password",
		Short:   "Rotate the database root/superuser password (revealed once)",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dbEngineValidCLI(engine) {
				return fmt.Errorf("--engine must be mariadb|postgres")
			}
			pw := ids.NewSecret()
			verb := "db.root.set_password"
			if engine == "postgres" {
				verb = "db.postgres.superuser.set_password"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if _, err := sharedAgent.Call(ctx, verb, map[string]any{"new_password": pw}); err != nil {
				cliAuditErr(ctx, "database.root_password_rotate", "database_engine", engine, nil)
				return fmt.Errorf("agent %s: %w", verb, err)
			}
			cliAuditOK(ctx, "database.root_password_rotate", "database_engine", engine, nil)
			if jsonOutput {
				return printJSON(map[string]any{"engine": engine, "password": pw})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rotated %s root/superuser password.\nNew password: %s\n(Shown once — store it now.)\n", engine, pw)
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "mariadb|postgres")
	return cmd
}

func registerDBOpsCmds(parent *cobra.Command) {
	parent.AddCommand(
		newDBBackupCmd(), newDBRestoreCmd(), newDBProcessesCmd(), newDBKillCmd(),
		newDBMaintenanceCmd(), newDBMaintenanceStatusCmd(), newDBRootPasswordCmd(),
		newDBConfigCmd(),
	)
}

func newDBConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "View / set database tuning (mariadb/postgres)"}
	cmd.AddCommand(newDBConfigGetCmd(), newDBConfigSetCmd())
	return cmd
}

func newDBConfigGetCmd() *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:     "get",
		Short:   "Show tuning params + current values (allowlist)",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dbEngineValidCLI(engine) {
				return fmt.Errorf("--engine must be mariadb|postgres")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			rows, err := repository.NewDBAdminRepository(sharedDB).ListTuning(ctx, engine)
			if err != nil {
				return err
			}
			persisted := make(map[string]string, len(rows))
			for _, r := range rows {
				persisted[r.Param] = r.Value
			}
			type paramOut struct {
				Param, Value, Default, Kind, Unit string
				RestartRequired                   bool
				Help                              string
			}
			out := make([]paramOut, 0)
			for _, p := range dbtuning.List(engine) {
				v := p.Default
				if pv, ok := persisted[p.Name]; ok {
					v = pv
				}
				out = append(out, paramOut{p.Name, v, p.Default, string(p.Kind), p.Unit, p.RestartRequired, p.Help})
			}
			if jsonOutput {
				return printJSON(out)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "PARAM\tVALUE\tDEFAULT\tKIND\tRESTART\tHELP")
			for _, o := range out {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\t%s\n", o.Param, o.Value, o.Default, o.Kind, o.RestartRequired, o.Help)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "mariadb|postgres")
	return cmd
}

func newDBConfigSetCmd() *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:     "set <param=value> [<param=value>...]",
		Short:   "Set tuning params (allowlist-validated; applies + may restart the engine)",
		Args:    cobra.MinimumNArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dbEngineValidCLI(engine) {
				return fmt.Errorf("--engine must be mariadb|postgres")
			}
			settings := map[string]string{}
			for _, a := range args {
				k, v, ok := strings.Cut(a, "=")
				if !ok || k == "" {
					return fmt.Errorf("argument %q is not param=value", a)
				}
				settings[strings.TrimSpace(k)] = v
			}
			if err := dbtuning.ValidateSet(engine, settings); err != nil {
				return fmt.Errorf("invalid value: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			repo := repository.NewDBAdminRepository(sharedDB)
			// Persist first (DB = source of truth), then apply the FULL desired set.
			for k, v := range settings {
				if err := repo.UpsertTuning(ctx, engine, k, v); err != nil {
					return fmt.Errorf("persist %s: %w", k, err)
				}
			}
			rows, err := repo.ListTuning(ctx, engine)
			if err != nil {
				return err
			}
			desired := make(map[string]string, len(rows))
			for _, r := range rows {
				desired[r.Param] = r.Value
			}
			verb := "db.config.apply"
			if engine == "postgres" {
				verb = "db.postgres.config.apply"
			}
			restart := dbtuning.RestartRequired(engine, desired)
			if restart {
				fmt.Fprintln(cmd.OutOrStdout(), "Applying — one or more params require an engine restart…")
			}
			if _, aerr := sharedAgent.Call(ctx, verb, map[string]any{
				"settings": desired, "restart_required": restart,
			}); aerr != nil {
				cliAuditErr(ctx, "database.config_apply", "database_engine", engine, nil)
				if strings.Contains(aerr.Error(), "UNRECOVERABLE") {
					return fmt.Errorf("UNRECOVERABLE: %s did not recover after apply+rollback — see /var/lib/jabali-agent/db-config-broken.json: %w", engine, aerr)
				}
				return fmt.Errorf("agent %s (change rejected/rolled back): %w", verb, aerr)
			}
			_ = repo.MarkTuningApplied(ctx, engine, "cli", time.Now().UTC())
			cliAuditOK(ctx, "database.config_apply", "database_engine", engine, nil)
			fmt.Fprintf(cmd.OutOrStdout(), "Applied %d %s tuning param(s)%s.\n", len(settings), engine, map[bool]string{true: " (engine restarted)", false: ""}[restart])
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "mariadb|postgres")
	return cmd
}
