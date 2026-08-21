// PHP pool CRUD + ini-override management CLI (Gitea #554). Mirrors the
// /php-pools API (php_pools.go): create/update/delete pools and CRUD ini
// overrides. Each mutation persists the same rows the handlers do, then runs
// the same php.pool.apply agent reconcile (replicated from
// reconcilePHPPoolViaAgent). Mutations are CLI-audited; inventory honours --json.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcilePHPPoolCLI fires php.pool.apply for a pool, replicating
// api.reconcilePHPPoolViaAgent (load user, build admin values/flags, apply,
// persist status).
func reconcilePHPPoolCLI(ctx context.Context, pool *models.PHPPool) error {
	pools := repository.NewPHPPoolRepository(sharedDB)
	user, err := repository.NewUserRepository(sharedDB).FindByID(ctx, pool.UserID)
	if err != nil || user == nil || user.Username == nil {
		return fmt.Errorf("resolve pool owner: %w", err)
	}
	overrides, err := repository.NewPHPPoolIniOverrideRepository(sharedDB).ListByPool(ctx, pool.ID)
	if err != nil {
		return fmt.Errorf("list overrides: %w", err)
	}
	adminValues := []map[string]string{}
	adminFlags := []map[string]string{}
	for _, o := range overrides {
		kv := map[string]string{"name": o.Directive, "value": o.Value}
		if o.Kind == "flag" {
			adminFlags = append(adminFlags, kv)
		} else {
			adminValues = append(adminValues, kv)
		}
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, aerr := sharedAgent.Call(actx, "php.pool.apply", map[string]any{
		"username":                     *user.Username,
		"php_version":                  pool.PHPVersion,
		"pm_mode":                      pool.PmMode,
		"pm_max_children":              pool.PmMaxChildren,
		"process_idle_timeout_seconds": pool.ProcessIdleTimeoutSeconds,
		"admin_values":                 adminValues,
		"admin_flags":                  adminFlags,
	})
	if aerr != nil {
		msg := "agent failed: " + aerr.Error()
		_ = pools.SetStatus(ctx, pool.ID, "error", &msg)
		return aerr
	}
	_ = pools.SetStatus(ctx, pool.ID, "ready", nil)
	return nil
}

func newPHPPoolListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all PHP-FPM pools",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			rows, _, err := repository.NewPHPPoolRepository(sharedDB).ListAll(ctx, repository.ListOptions{})
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tUSER_ID\tVERSION\tPM_MODE\tMAX_CHILDREN\tIDLE\tSTATUS")
			for _, p := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", p.ID, p.UserID, p.PHPVersion, p.PmMode, p.PmMaxChildren, p.ProcessIdleTimeoutSeconds, p.Status)
			}
			return tw.Flush()
		},
	}
}

func newPHPPoolCreateCmd() *cobra.Command {
	var (
		user        string
		version     string
		pmMode      string
		maxChildren uint32
		idleTimeout uint32
	)
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a PHP-FPM pool for a user",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if user == "" || version == "" {
				return fmt.Errorf("--user and --php-version are required")
			}
			switch pmMode {
			case "ondemand", "dynamic", "static":
			default:
				return fmt.Errorf("--pm-mode must be ondemand|dynamic|static")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			u, err := resolveTenantUser(ctx, user)
			if err != nil {
				return err
			}
			repo := repository.NewPHPPoolRepository(sharedDB)
			// JAB-174: a user already having a pool means `create` would leave a
			// duplicate + orphan (the old row stays `active` with no on-disk
			// config, and `pool get` returns it). Switching a user's PHP version
			// is `pool set` (updates in place + re-applies); per-domain versions
			// are `domain php-version set`. So refuse to create a second pool and
			// point at the right command instead of silently orphaning one.
			if existing, _ := repo.FindByUserID(ctx, u.ID); existing != nil {
				if existing.PHPVersion == version {
					return fmt.Errorf("pool already exists for %s php %s (id=%s)", user, version, existing.ID)
				}
				return fmt.Errorf("user %s already has a PHP pool on php %s (id=%s); to switch it use `jabali php pool set --user %s --version %s` (updates in place — no duplicate/orphan), or `jabali domain php-version set <domain> %s` for a per-domain version",
					user, existing.PHPVersion, existing.ID, user, version, version)
			}
			pool := &models.PHPPool{
				ID: ulid.Make().String(), UserID: u.ID, PHPVersion: version,
				PmMode: pmMode, PmMaxChildren: maxChildren, ProcessIdleTimeoutSeconds: idleTimeout,
				Status: "pending",
			}
			if err := repo.Create(ctx, pool); err != nil {
				return fmt.Errorf("create pool: %w", err)
			}
			if err := reconcilePHPPoolCLI(ctx, pool); err != nil {
				return fmt.Errorf("pool created but apply failed: %w", err)
			}
			cliAuditOK(ctx, "php_pool.create", "php_pool", pool.ID, &u.ID)
			if jsonOutput {
				return printJSON(pool)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created pool %s (%s php %s)\n", pool.ID, user, version)
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "owner (id or username) (required)")
	cmd.Flags().StringVar(&version, "php-version", "", "PHP version e.g. 8.3 (required)")
	cmd.Flags().StringVar(&pmMode, "pm-mode", "ondemand", "ondemand|dynamic|static")
	cmd.Flags().Uint32Var(&maxChildren, "pm-max-children", 20, "pm.max_children")
	cmd.Flags().Uint32Var(&idleTimeout, "idle-timeout", 60, "process idle timeout (seconds)")
	return cmd
}

func newPHPPoolDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <pool-id>",
		Short:   "Delete a PHP-FPM pool and its ini overrides",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("deleting a pool affects the owner's PHP sites; re-run with --force")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			poolRepo := repository.NewPHPPoolRepository(sharedDB)
			pool, err := poolRepo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("pool not found: %w", err)
			}
			// JAB-342: refuse deletion while any domain still references this pool,
			// mirroring the HTTP handler's 409 pool_has_bound_domains. Deleting the
			// row out from under bound domains leaves them pointing at a pool that
			// no longer exists (dangling binding); the operator must unbind first.
			bound, cerr := repository.NewDomainRepository(sharedDB).CountByPHPPoolID(ctx, pool.ID)
			if cerr != nil {
				return fmt.Errorf("count domains bound to pool: %w", cerr)
			}
			if bound > 0 {
				return fmt.Errorf("pool %s has %d bound domain(s) — unbind them before deleting", pool.ID, bound)
			}
			// Resolve the owner before deleting so we can remove the live FPM pool.
			owner, uerr := repository.NewUserRepository(sharedDB).FindByID(ctx, pool.UserID)
			if uerr != nil || owner == nil || owner.Username == nil {
				return fmt.Errorf("resolve pool owner: %w", uerr)
			}
			ovRepo := repository.NewPHPPoolIniOverrideRepository(sharedDB)
			if ovs, lerr := ovRepo.ListByPool(ctx, pool.ID); lerr == nil {
				for _, o := range ovs {
					_ = ovRepo.Delete(ctx, o.ID)
				}
			}
			if err := poolRepo.Delete(ctx, pool.ID); err != nil {
				return fmt.Errorf("delete pool: %w", err)
			}
			cliAuditOK(ctx, "php_pool.delete", "php_pool", pool.ID, &pool.UserID)
			// JAB-342: remove the live FPM pool on the host too, mirroring the HTTP
			// handler's php.pool.remove. Without this the pool's socket + config
			// linger under the FPM tree after its row is gone (orphaned config).
			if _, aerr := sharedAgent.Call(ctx, "php.pool.remove", map[string]any{"username": *owner.Username}); aerr != nil {
				return fmt.Errorf("pool row deleted but host pool removal failed (re-run to retry): %w", aerr)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted pool %s (host pool removed)\n", pool.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func newPHPPoolIniCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ini", Short: "Manage a pool's php.ini overrides"}
	cmd.AddCommand(newPHPPoolIniListCmd(), newPHPPoolIniAddCmd(), newPHPPoolIniUpdateCmd(), newPHPPoolIniRemoveCmd())
	return cmd
}

func newPHPPoolIniListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <pool-id>",
		Short:   "List a pool's ini overrides",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			rows, err := repository.NewPHPPoolIniOverrideRepository(sharedDB).ListByPool(ctx, args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tDIRECTIVE\tVALUE\tKIND")
			for _, o := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", o.ID, o.Directive, o.Value, o.Kind)
			}
			return tw.Flush()
		},
	}
}

func newPHPPoolIniAddCmd() *cobra.Command {
	var directive, value, kind string
	cmd := &cobra.Command{
		Use:     "add <pool-id>",
		Short:   "Add an ini override to a pool",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if directive == "" {
				return fmt.Errorf("--directive is required")
			}
			if kind != "value" && kind != "flag" {
				return fmt.Errorf("--kind must be value|flag")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			poolRepo := repository.NewPHPPoolRepository(sharedDB)
			pool, err := poolRepo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("pool not found: %w", err)
			}
			ov := &models.PHPPoolIniOverride{ID: ulid.Make().String(), PoolID: pool.ID, Directive: directive, Value: value, Kind: kind}
			if err := repository.NewPHPPoolIniOverrideRepository(sharedDB).Create(ctx, ov); err != nil {
				return fmt.Errorf("create override: %w", err)
			}
			if err := reconcilePHPPoolCLI(ctx, pool); err != nil {
				return fmt.Errorf("override saved but apply failed: %w", err)
			}
			cliAuditOK(ctx, "php_pool.ini_add", "php_pool_ini", ov.ID, &pool.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s=%s (%s) to pool %s\n", directive, value, kind, pool.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&directive, "directive", "", "php.ini directive (required)")
	cmd.Flags().StringVar(&value, "value", "", "value (for kind=value) or on/off (for kind=flag)")
	cmd.Flags().StringVar(&kind, "kind", "value", "value|flag")
	return cmd
}

func newPHPPoolIniUpdateCmd() *cobra.Command {
	var value string
	cmd := &cobra.Command{
		Use:     "update <override-id>",
		Short:   "Update an ini override's value",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			ovRepo := repository.NewPHPPoolIniOverrideRepository(sharedDB)
			ov, err := ovRepo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("override not found: %w", err)
			}
			ov.Value = value
			if err := ovRepo.Update(ctx, ov); err != nil {
				return fmt.Errorf("update override: %w", err)
			}
			pool, perr := repository.NewPHPPoolRepository(sharedDB).FindByID(ctx, ov.PoolID)
			if perr != nil {
				return fmt.Errorf("load pool: %w", perr)
			}
			if err := reconcilePHPPoolCLI(ctx, pool); err != nil {
				return fmt.Errorf("override updated but apply failed: %w", err)
			}
			cliAuditOK(ctx, "php_pool.ini_update", "php_pool_ini", ov.ID, &pool.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Updated override %s\n", ov.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "new value")
	return cmd
}

func newPHPPoolIniRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <override-id>",
		Short:   "Remove an ini override",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			ovRepo := repository.NewPHPPoolIniOverrideRepository(sharedDB)
			ov, err := ovRepo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("override not found: %w", err)
			}
			poolID := ov.PoolID
			if err := ovRepo.Delete(ctx, ov.ID); err != nil {
				return fmt.Errorf("delete override: %w", err)
			}
			pool, perr := repository.NewPHPPoolRepository(sharedDB).FindByID(ctx, poolID)
			if perr != nil {
				return fmt.Errorf("load pool: %w", perr)
			}
			if err := reconcilePHPPoolCLI(ctx, pool); err != nil {
				return fmt.Errorf("override removed but apply failed: %w", err)
			}
			cliAuditOK(ctx, "php_pool.ini_remove", "php_pool_ini", ov.ID, &pool.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Removed override %s\n", ov.ID)
			return nil
		},
	}
}
