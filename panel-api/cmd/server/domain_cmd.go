package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage hosted domains",
	}
	cmd.AddCommand(
		newDomainListCmd(),
		newDomainShowCmd(),
		newDomainIPACLCmd(),
		newDomainWhoisCmd(),
		newDomainBrowseCmd(),
		newDomainSetCmd(),
		newDomainSkipAutoSANCmd(),
		newDomainMTAStsCmd(),
		newDomainHtaccessPreviewCmd(),
		newDomainBandwidthCmd(),
		newDomainCreateCmd(),
		newDomainEnableCmd(),
		newDomainDisableCmd(),
		newDomainDeleteCmd(),
		newDomainChownCmd(),
		newDomainPruneOrphansCmd(),
	)
	// M6 email-* leaves live in their own file (domain_email_cmd.go).
	cmd.AddCommand(newDomainFixPermsCmd())
	cmd.AddCommand(domainEmailSubcommands()...)
	// M6.5 catchall + disclaimer (domain_extras_cmd.go).
	cmd.AddCommand(domainExtraSubcommands()...)
	// GH #329 per-domain PHP version (domain_php_version_cmd.go).
	cmd.AddCommand(domainPHPVersionSubcommands()...)
	cmd.AddCommand(domainPHPSettingsSubcommands()...)
	cmd.AddCommand(domainDirectoryPrivacySubcommands()...)
	return cmd
}

// ---- list ----

func newDomainListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List domains (direct DB — M20-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			domains, err := listDomainsDirect(ctx)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]interface{}{
					"domains": domains,
					"total":   len(domains),
				})
			}

			if len(domains) == 0 {
				fmt.Println("No domains found")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tUSER_ID\tENABLED\tDOC_ROOT")
			for _, d := range domains {
				enabled := "no"
				if d.IsEnabled {
					enabled = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					d.ID, d.Name, d.UserID, enabled, d.DocRoot)
			}
			return w.Flush()
		},
	}
}

// ---- create ----

func newDomainCreateCmd() *cobra.Command {
	var name, userID, docRoot string
	var reverseProxy bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new domain (direct DB; bypasses HTTP auth — M20-safe)",
		Long: `Create a new domain row + auto-enable email when the agent is reachable.

The owner must be a non-admin user with a POSIX username. Admin users
(created via 'jabali user create --admin') intentionally cannot host
domains: admins have no /home/<user> tree to anchor the docroot, no
slice for resource limits, and no SFTP gate. Create a regular user
first, then assign domains to that account.

Domain validation: must be a valid FQDN (≥2 labels, 2+ letter TLD,
no IP literals). Bare hostnames like 'invalid' are rejected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 30s — the inline auto-enable step makes an agent round trip
			// (DKIM keypair gen + Stalwart register) on top of the DB
			// insert. 10s would be tight if the agent is slow to respond.
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			d, warnings, err := createDomainDirect(ctx, cliDomainInput{
				Name:         name,
				UserID:       userID,
				DocRoot:      docRoot,
				ReverseProxy: reverseProxy,
			})
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"domain":   d,
					"warnings": warnings,
				})
			}
			cliAuditOK(ctx, "domain.create", "domain", d.ID, &d.UserID)
			fmt.Printf("Domain created: %s (ID: %s)\n", d.Name, d.ID)
			if d.ReverseProxyPort > 0 {
				fmt.Printf("Reverse proxy: forwards to 127.0.0.1:%d — run your app on that port.\n", d.ReverseProxyPort)
			}
			if d.EmailEnabled {
				selector := ""
				if d.DkimSelector != nil {
					selector = *d.DkimSelector
				}
				fmt.Printf("Email enabled automatically (DKIM selector: %s).\n", selector)
			}
			for _, w := range warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}
			fmt.Printf("Note: reconciler will materialise the nginx vhost within %s.\n",
				sharedCfg.Agent.ReconcilerInterval)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Domain name (required)")
	cmd.Flags().StringVar(&userID, "user", "", "User email, username, or ULID (required)")
	cmd.Flags().StringVar(&docRoot, "doc-root", "", "Document root (optional, auto-generated if not provided)")
	cmd.Flags().BoolVar(&reverseProxy, "reverse-proxy", false, "Make this a reverse-proxy domain: allocate a loopback port and proxy '/' to it (GH #1175)")
	return cmd
}

// ---- enable ----

func newDomainEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "enable <domain-name|domain-id>",
		Short:   "Enable a domain (direct DB — M20-safe)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			target, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			d, err := setDomainEnabledDirect(ctx, target.ID, true)
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "domain.enable", "domain", d.ID, &d.UserID)
			fmt.Printf("Domain %s enabled\n", d.Name)
			return nil
		},
	}
}

// ---- disable ----

func newDomainDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "disable <domain-name|domain-id>",
		Short:   "Disable a domain (direct DB — M20-safe)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			target, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			d, err := setDomainEnabledDirect(ctx, target.ID, false)
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "domain.disable", "domain", d.ID, &d.UserID)
			fmt.Printf("Domain %s disabled\n", d.Name)
			return nil
		},
	}
}

// ---- delete ----

func newDomainDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <domain-name|domain-id>",
		Short: "Delete a domain (direct DB; reconciler tears down nginx — M20-safe)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			// Fetch first so the confirmation shows the name + we can print
			// it after the row is gone.
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			d, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}

			if !force {
				fmt.Printf("Delete domain %s? (type 'yes' to confirm): ", d.Name)
				var confirm string
				fmt.Scanln(&confirm)
				if !strings.EqualFold(confirm, "yes") {
					fmt.Println("Cancelled")
					return nil
				}
			}

			_, teardownPending, err := deleteDomainDirect(ctx, d.ID)
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "domain.delete", "domain", d.ID, &d.UserID)
			if teardownPending {
				fmt.Printf("Domain %s deleted; host-side teardown is PENDING (agent unreachable or teardown failed) — the reconciler retries it until it succeeds\n", d.Name)
			} else {
				fmt.Printf("Domain %s deleted (nginx vhost, mail accounts, and DNS zone torn down)\n", d.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}
