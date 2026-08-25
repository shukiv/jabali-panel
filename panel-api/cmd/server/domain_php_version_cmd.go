// domain_php_version_cmd.go — GH #329 CLI parity for per-domain PHP version.
// Mirrors POST /domains/:id/php-pool { php_version } but goes direct DB so
// operators can drive it from a script without a Kratos session. The
// reconciler applies the (versioned) pool + regenerates the vhost within a
// tick, same as the web path.

package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cliDomainPHPVersionRe guards the version format — the value flows into an
// FPM pool slug + systemd instance name downstream, so it must be X.Y only.
var cliDomainPHPVersionRe = regexp.MustCompile(`^\d+\.\d+$`)

// domainPHPVersionSubcommands returns the `domain php-version` group.
func domainPHPVersionSubcommands() []*cobra.Command {
	return []*cobra.Command{newDomainPHPVersionCmd()}
}

func newDomainPHPVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "php-version",
		Short: "Manage a domain's PHP version (per-domain FPM pool, GH #329)",
	}
	cmd.AddCommand(newDomainPHPVersionShowCmd(), newDomainPHPVersionSetCmd())
	return cmd
}

func newDomainPHPVersionShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <domain-name-or-id>",
		Short:   "Show a domain's bound PHP version",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			if dom.PHPPoolID == nil {
				if jsonOutput {
					return printJSON(map[string]any{"domain": dom.Name, "php_version": nil, "static": true})
				}
				fmt.Printf("%s: static (no PHP)\n", dom.Name)
				return nil
			}
			pool, err := repository.NewPHPPoolRepository(sharedDB).FindByID(ctx, *dom.PHPPoolID)
			if err != nil {
				return fmt.Errorf("load pool: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"domain": dom.Name, "php_version": pool.PHPVersion, "pool_id": pool.ID, "status": pool.Status})
			}
			fmt.Printf("%s: PHP %s (pool %s, %s)\n", dom.Name, pool.PHPVersion, pool.ID, pool.Status)
			return nil
		},
	}
}

func newDomainPHPVersionSetCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:     "set <domain-name-or-id> --version X.Y",
		Short:   "Bind a domain to a PHP version (find-or-create the pool; reconciler converges)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if !cliDomainPHPVersionRe.MatchString(version) {
				return fmt.Errorf("--version must be X.Y (e.g. 8.2)")
			}
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			pools := repository.NewPHPPoolRepository(sharedDB)
			list, err := pools.ListByUserID(ctx, dom.UserID)
			if err != nil {
				return fmt.Errorf("list pools: %w", err)
			}
			if len(list) == 0 {
				return fmt.Errorf("user has no PHP pool yet; run a reconcile first")
			}
			defaultPool := &list[0] // earliest = default (slug == username)

			var pool *models.PHPPool
			switch {
			case version == defaultPool.PHPVersion:
				pool = defaultPool
			default:
				existing, ferr := pools.FindByUserAndVersion(ctx, dom.UserID, version)
				if ferr == nil {
					pool = existing
				} else if errors.Is(ferr, repository.ErrNotFound) {
					// JAB-344: clone the COMPLETE tuning model via the shared
					// constructor. The CLI used to copy only pm_mode / max_children
					// / idle_timeout, silently resetting spare-servers, max-requests,
					// request-terminate-timeout, and performance_mode — so a CLI
					// version switch changed capacity/timeout behavior vs HTTP.
					pool = models.NewVersionedPHPPool(ids.NewULID(), version, defaultPool)
					if err := pools.Create(ctx, pool); err != nil {
						return fmt.Errorf("create pool: %w", err)
					}
				} else {
					return fmt.Errorf("lookup pool: %w", ferr)
				}
			}

			if err := domainRepoFromDB().SetPHPPoolID(ctx, dom.ID, &pool.ID); err != nil {
				cliAuditErr(ctx, "domain.php_version.set", "domain", dom.Name, &dom.UserID)
				return fmt.Errorf("bind domain: %w", err)
			}
			cliAuditOK(ctx, "domain.php_version.set", "domain", dom.Name, &dom.UserID)
			fmt.Printf("%s -> PHP %s (pool %s); reconciler converges within ~60s\n", dom.Name, version, pool.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "PHP version X.Y (e.g. 8.2); required")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}
