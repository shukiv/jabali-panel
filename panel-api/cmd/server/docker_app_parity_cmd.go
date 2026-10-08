// Docker app CLI lifecycle parity with the admin API/GUI (Gitea #546).
// Adds the operational verbs the GUI exposes but the CLI lacked: env
// get/set/regenerate, one-off exec, manual backup + restore, maintenance
// disk-usage/prune, and a settings patch. Each reuses the same agent verbs and
// re-render path the REST handlers use (docker_app_env.go / docker_apps.go), so
// validation, tenant-validation gating, and audit semantics match.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// validateEnvKVCLI mirrors api.validateEnvKV (key charset + value caps).
func validateEnvKVCLI(k, v string) string {
	if k == "" {
		return "empty key"
	}
	for _, r := range k {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return "key " + k + " has an invalid character (allowed: A-Z a-z 0-9 _)"
		}
	}
	if len(v) > 4096 {
		return "value for " + k + " is too long"
	}
	if strings.ContainsAny(v, "\n\r\x00") {
		return "value for " + k + " may not contain newlines or NUL"
	}
	return ""
}

// cliApplyTenantValidate sets tenant_validate/tenant_caps/tenant_cgroup for a
// tenant-owned app, failing closed when the safety profile can't be resolved
// (Gitea #529). No-op for admin/server-owned apps. Mirrors
// api.applyTenantValidateParams.
func cliApplyTenantValidate(ctx context.Context, app *models.DockerApp, params map[string]any) error {
	if app == nil || app.UserID == nil {
		return nil
	}
	cat, err := loadDockerCatalogForCLI()
	if err != nil {
		return fmt.Errorf("catalog unavailable: cannot resolve tenant validation metadata: %w", err)
	}
	entry, ok := cat.Get(app.Slug)
	if !ok {
		return fmt.Errorf("catalog entry %q not found: cannot resolve tenant validation metadata", app.Slug)
	}
	u, uerr := repository.NewUserRepository(sharedDB).FindByID(ctx, *app.UserID)
	if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
		return fmt.Errorf("cannot resolve tenant owner slice for app %s: %v", app.ID, uerr)
	}
	params["tenant_validate"] = true
	params["tenant_caps"] = dockerapp.TenantCapAllowlist(entry.TenantCaps)
	params["tenant_cgroup"] = "jabali-user-" + *u.Username + ".slice"
	return nil
}

func applyDockerEnvWithBase(ctx context.Context, repo repository.DockerAppRepository, app *models.DockerApp, baseEnv map[string]string) error {
	compose, envFile, tenantServices, err := renderInstallComposeCLI(ctx, repo, app, baseEnv, false)
	if err != nil {
		return err
	}
	params := map[string]any{
		"slug":                        app.EffectiveSlug(),
		"compose_yml":                 compose,
		"env_file":                    envFile,
		"healthcheck_timeout_seconds": 300,
	}
	if verr := cliApplyTenantValidate(ctx, app, params); verr != nil {
		return verr
	}
	if tenantServices != nil {
		params["tenant_services"] = tenantServices
	}
	raw, err := sharedAgent.Call(ctx, "docker_app.update", params)
	if err != nil {
		return err
	}
	// A recreate onto a new image keeps the version label on it (GH #1956).
	var outc struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(raw, &outc) == nil && outc.Outcome == "updated" {
		if cat, cerr := loadDockerCatalogForCLI(); cerr == nil {
			if entry, ok := cat.Get(app.Slug); ok {
				if target, terr := entry.TargetFor(app.CatalogVersion, false); terr == nil && target.Version != "" && target.Version != app.CatalogVersion {
					_ = repo.UpdateCatalogVersion(ctx, app.ID, target.Version)
				}
			}
		}
	}
	os.Stdout.Write(raw)
	os.Stdout.Write([]byte{'\n'})
	return nil
}

func newDockerAppEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "View, edit, or regenerate a Docker app's environment",
	}
	cmd.AddCommand(newDockerAppEnvGetCmd(), newDockerAppEnvSetCmd(), newDockerAppEnvRegenerateCmd())
	return cmd
}

func newDockerAppEnvGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "List the app's environment (reveals secrets)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			app, err := dockerAppRepoFromDB().FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			env, err := readInstallEnvCLI(ctx, app)
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(env)
			}
			for k, v := range env {
				fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", k, v)
			}
			return nil
		},
	}
}

func newDockerAppEnvSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <id> <KEY=VALUE> [<KEY=VALUE>...]",
		Short:   "Edit env values, re-render the compose, and recreate the container",
		Args:    cobra.MinimumNArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			overrides := map[string]string{}
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("argument %q is not KEY=VALUE", kv)
				}
				if msg := validateEnvKVCLI(k, v); msg != "" {
					return fmt.Errorf("invalid env: %s", msg)
				}
				overrides[k] = v
			}
			existing, err := readInstallEnvCLI(ctx, app)
			if err != nil {
				return err
			}
			for k, v := range overrides {
				existing[k] = v
			}
			return applyDockerEnvWithBase(ctx, repo, app, existing)
		},
	}
}

func newDockerAppEnvRegenerateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "regenerate <id> <KEY>",
		Short:   "Mint a fresh value for a generated secret, then recreate the container",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			key := args[1]
			cat, err := loadDockerCatalogForCLI()
			if err != nil {
				return err
			}
			entry, ok := cat.Get(app.Slug)
			if !ok {
				return fmt.Errorf("catalog entry %q not found", app.Slug)
			}
			generated := false
			for _, ev := range entry.Env {
				if ev.Name == key && ev.Generate != "" {
					generated = true
					break
				}
			}
			if !generated {
				return fmt.Errorf("key %q is not a generated secret", key)
			}
			existing, err := readInstallEnvCLI(ctx, app)
			if err != nil {
				return err
			}
			delete(existing, key) // absent → MaterialiseEnv generates fresh
			return applyDockerEnvWithBase(ctx, repo, app, existing)
		},
	}
}

func newDockerAppExecCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:     "exec <id> -- <command...>",
		Short:   "Run a one-off command in the app's container",
		Args:    cobra.MinimumNArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			app, err := dockerAppRepoFromDB().FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			command := strings.Join(args[1:], " ")
			raw, err := sharedAgent.Call(ctx, "docker_app.exec", map[string]any{
				"slug":    app.EffectiveSlug(),
				"service": service,
				"command": command,
			})
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "compose service to exec into (default: primary)")
	return cmd
}

func newDockerAppBackupCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "backup-create <id>",
		Short:   "Create a manual backup of the app",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			app, err := dockerAppRepoFromDB().FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			params := map[string]any{"slug": app.EffectiveSlug(), "reason": "manual"}
			if app.BackupDestinationID != nil {
				params["destination_id"] = *app.BackupDestinationID
			}
			raw, err := sharedAgent.Call(ctx, "docker_app.backup", params)
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newDockerAppRestoreCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "restore <id> <snapshot-id>",
		Short:   "Restore a selected backup snapshot (replaces the app's data)",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("restore replaces the app's data; re-run with --force")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			params := map[string]any{"slug": app.EffectiveSlug(), "snapshot_id": args[1]}
			if app.BackupDestinationID != nil {
				params["destination_id"] = *app.BackupDestinationID
			}
			if verr := cliApplyTenantValidate(ctx, app, params); verr != nil {
				return verr
			}
			raw, err := sharedAgent.Call(ctx, "docker_app.restore", params)
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm the destructive restore")
	return cmd
}

func newDockerAppMaintenanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Docker disk-usage report and prune",
	}
	cmd.AddCommand(newDockerAppDiskUsageCmd(), newDockerAppPruneCmd())
	return cmd
}

func newDockerAppDiskUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "disk-usage",
		Short:   "Report Docker disk usage",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, "docker.disk_usage", map[string]any{})
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newDockerAppPruneCmd() *cobra.Command {
	var (
		volumes bool
		all     bool
		force   bool
	)
	cmd := &cobra.Command{
		Use:     "prune",
		Short:   "Prune unused Docker resources (reclaims disk)",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !force {
				return fmt.Errorf("prune removes unused Docker data; re-run with --force")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, "docker.prune", map[string]any{
				"volumes": volumes,
				"all":     all,
			})
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also prune unused volumes")
	cmd.Flags().BoolVar(&all, "all", false, "remove all unused images, not just dangling")
	cmd.Flags().BoolVar(&force, "force", false, "confirm the prune")
	return cmd
}

func newDockerAppPatchCmd() *cobra.Command {
	var (
		updateMode string
		cpu        string
		memory     string
		pids       int
	)
	cmd := &cobra.Command{
		Use:     "patch <id>",
		Short:   "Patch app settings (update mode, CPU/memory/PID limits)",
		Long:    "Patch resource limits and update mode. Port/domain changes go through `docker-app install`/edit (they require a full compose re-render with the container stopped).",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			changed := false
			if cmd.Flags().Changed("update-mode") {
				if updateMode != models.DockerAppUpdateModeManual && updateMode != models.DockerAppUpdateModeAuto {
					return fmt.Errorf("--update-mode must be %q or %q", models.DockerAppUpdateModeManual, models.DockerAppUpdateModeAuto)
				}
				app.UpdateMode = updateMode
				changed = true
			}
			if cmd.Flags().Changed("cpu") {
				app.CPULimit = &cpu
				changed = true
			}
			if cmd.Flags().Changed("memory") {
				app.MemoryLimit = &memory
				changed = true
			}
			if cmd.Flags().Changed("pids") {
				app.PIDsLimit = &pids
				changed = true
			}
			if !changed {
				return fmt.Errorf("no changes specified (use --update-mode/--cpu/--memory/--pids)")
			}
			if err := repo.Update(ctx, app); err != nil {
				return fmt.Errorf("persist: %w", err)
			}
			if jsonOutput {
				return printJSON(app)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Patched %s. Re-create the container (docker-app update) to apply new resource limits.\n", app.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&updateMode, "update-mode", "", "update mode: manual|auto")
	cmd.Flags().StringVar(&cpu, "cpu", "", "CPU limit (e.g. 1.5; empty to clear)")
	cmd.Flags().StringVar(&memory, "memory", "", "memory limit (e.g. 512m; empty to clear)")
	cmd.Flags().IntVar(&pids, "pids", 0, "PIDs limit")
	return cmd
}
