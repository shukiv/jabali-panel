// Advanced domain settings CLI (Gitea #558). Ships the clean, fully-faithful
// slices: `domain show` (full advanced-settings state) and `domain ip-acl`
// CRUD (the per-domain IP allow/deny list the nginx snippet renders from).
//
// The reconcile-coupled mutation set — nginx options/rules, redirects, cache,
// directory index, SSL mode — is deliberately NOT shipped here: those apply via
// the in-process domain Reconciler (Schedule/ReconcileSSLInline), and a thin CLI
// repo-write would not reproduce the GUI's reconcile/reload behavior (criterion
// 6). Tracked for a dedicated follow-up that wires the reconcile path properly.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// resolveDomainCLI resolves a domain by ULID then by name.
func resolveDomainCLI(ctx context.Context, ref string) (*models.Domain, error) {
	repo := domainRepoFromDB()
	if d, err := repo.FindByID(ctx, ref); err == nil && d != nil {
		return d, nil
	}
	d, err := repo.FindByName(ctx, ref)
	if err != nil || d == nil {
		return nil, fmt.Errorf("no domain with id or name %q", ref)
	}
	return d, nil
}

func newDomainShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <domain-name|domain-id>",
		Short:   "Show a domain's full advanced-settings state (JSON)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			acls, _ := repository.NewDomainIPACLRepository(sharedDB).ListByDomain(ctx, d.ID)
			if jsonOutput {
				return printJSON(map[string]any{"domain": d, "ip_acls": acls})
			}
			return printJSON(map[string]any{"domain": d, "ip_acls": acls})
		},
	}
}

func newDomainIPACLCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ip-acl", Short: "Manage a domain's IP allow/deny ACL"}
	cmd.AddCommand(newDomainIPACLListCmd(), newDomainIPACLAddCmd(), newDomainIPACLDeleteCmd())
	return cmd
}

func newDomainIPACLListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <domain>",
		Short:   "List a domain's IP ACL entries",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			rows, err := repository.NewDomainIPACLRepository(sharedDB).ListByDomain(ctx, d.ID)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tACTION\tCIDR\tPRIORITY\tCOMMENT")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", r.ID, r.Action, r.CIDR, r.Priority, r.Comment)
			}
			return tw.Flush()
		},
	}
}

func newDomainIPACLAddCmd() *cobra.Command {
	var cidr, action, comment string
	var priority int
	cmd := &cobra.Command{
		Use:     "add <domain>",
		Short:   "Add an IP ACL entry to a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if action != "allow" && action != "deny" {
				return fmt.Errorf("--action must be allow|deny")
			}
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				if net.ParseIP(cidr) == nil {
					return fmt.Errorf("--cidr %q is not a valid IP or CIDR", cidr)
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			row := &models.DomainIPACL{ID: ids.NewULID(), DomainID: d.ID, CIDR: cidr, Action: action, Priority: priority, Comment: comment}
			if err := repository.NewDomainIPACLRepository(sharedDB).Create(ctx, row); err != nil {
				return fmt.Errorf("create ACL: %w", err)
			}
			cliAuditOK(ctx, "domain.ip_acl_add", "domain_ip_acl", row.ID, &d.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s %s (id=%s). Converges on the next domain reconcile.\n", action, cidr, row.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&cidr, "cidr", "", "IP or CIDR (required)")
	cmd.Flags().StringVar(&action, "action", "deny", "allow|deny")
	cmd.Flags().IntVar(&priority, "priority", 0, "rule priority (lower = first)")
	cmd.Flags().StringVar(&comment, "comment", "", "optional comment")
	_ = cmd.MarkFlagRequired("cidr")
	return cmd
}

func newDomainIPACLDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <acl-id>",
		Short:   "Delete an IP ACL entry by id",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if err := repository.NewDomainIPACLRepository(sharedDB).Delete(ctx, args[0]); err != nil {
				return fmt.Errorf("delete ACL: %w", err)
			}
			cliAuditOK(ctx, "domain.ip_acl_delete", "domain_ip_acl", args[0], nil)
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted ACL %s\n", args[0])
			return nil
		},
	}
}

// --- #558 domain set (scalar advanced settings) + #583 skip-auto-san / mta-sts ---
// All are DB writes; the live panel-api reconciler ReconcileAll converges them
// (nginx re-render/reload, SSL re-issue, mta-sts DNS publish) within ~60s — same
// end-state as the GUI (feedback_cli_reconcile_apply).

func newDomainSetCmd() *cobra.Command {
	var (
		redirectTo    string
		redirectType  string
		indexPriority string
		nginxDirs     string
		sslMode       string
		cache         string
	)
	cmd := &cobra.Command{
		Use:     "set <domain-name|domain-id>",
		Short:   "Set advanced domain settings (redirect / nginx directives / index / ssl mode / cache)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			changed := false
			if cmd.Flags().Changed("redirect-all-to") {
				v := strings.TrimSpace(redirectTo)
				d.RedirectAllTo = &v
				changed = true
			}
			if cmd.Flags().Changed("redirect-type") {
				if redirectType != "permanent" && redirectType != "temporary" {
					return fmt.Errorf("--redirect-type must be permanent|temporary")
				}
				d.RedirectAllType = &redirectType
				changed = true
			}
			if cmd.Flags().Changed("index-priority") {
				d.IndexPriority = indexPriority
				changed = true
			}
			if cmd.Flags().Changed("nginx-directives") {
				v := nginxDirs
				d.NginxCustomDirectives = &v
				changed = true
			}
			if cmd.Flags().Changed("ssl-mode") {
				if sslMode == models.SSLModeCustom {
					return fmt.Errorf("--ssl-mode=custom is set by installing a custom cert, not here")
				}
				if !models.ValidSSLMode(sslMode) {
					return fmt.Errorf("--ssl-mode must be le|self|none")
				}
				d.SSLMode = sslMode
				d.SSLEnabled = models.SSLEnabledForMode(sslMode)
				changed = true
			}
			if cmd.Flags().Changed("cache") {
				b, perr := parseBool(cache)
				if perr != nil {
					return perr
				}
				d.CacheEnabled = b
				changed = true
			}
			if !changed {
				return fmt.Errorf("no settings specified (--redirect-all-to/--redirect-type/--index-priority/--nginx-directives/--ssl-mode/--cache)")
			}
			if err := domainRepoFromDB().Update(ctx, d); err != nil {
				return fmt.Errorf("update domain: %w", err)
			}
			// JAB-313: ssl_mode is authoritative (ADR-0141) but is NOT in the
			// general Update column allowlist (domain_repository.go: "SSL flags ...
			// have their own dedicated repo methods"), so the assignment above was
			// silently dropped — the command reported success while the mode never
			// changed and the reconciler kept the old TLS behavior. Persist it
			// through the dedicated writer, which also keeps ssl_enabled consistent,
			// exactly as the HTTP path and ssl_custom_cmd.go do.
			if cmd.Flags().Changed("ssl-mode") {
				if err := domainRepoFromDB().UpdateSSLMode(ctx, d.ID, sslMode); err != nil {
					return fmt.Errorf("persist ssl mode: %w", err)
				}
			}
			// JAB-313 sibling: cache_enabled is likewise excluded from the general
			// Update allowlist and has its own dedicated writer (ADR-0108), so
			// --cache was being silently dropped the same way. Persist it here too.
			if cmd.Flags().Changed("cache") {
				if err := domainRepoFromDB().UpdateCacheEnabled(ctx, d.ID, d.CacheEnabled); err != nil {
					return fmt.Errorf("persist cache setting: %w", err)
				}
			}
			cliAuditOK(ctx, "domain.settings_update", "domain", d.ID, &d.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Updated %s. Converges on the next reconcile pass (≤60s): nginx re-render/reload%s.\n",
				d.Name, map[bool]string{true: " + SSL re-issue", false: ""}[cmd.Flags().Changed("ssl-mode")])
			return nil
		},
	}
	cmd.Flags().StringVar(&redirectTo, "redirect-all-to", "", "redirect the whole domain to this URL ('' clears)")
	cmd.Flags().StringVar(&redirectType, "redirect-type", "", "permanent|temporary")
	cmd.Flags().StringVar(&indexPriority, "index-priority", "", "directory index priority (e.g. html_first, php_first)")
	cmd.Flags().StringVar(&nginxDirs, "nginx-directives", "", "raw custom nginx directives for the server block")
	cmd.Flags().StringVar(&sslMode, "ssl-mode", "", "le|self|none (custom = install a cert)")
	cmd.Flags().StringVar(&cache, "cache", "", "on|off — nginx fastcgi page cache")
	return cmd
}

func newDomainSkipAutoSANCmd() *cobra.Command {
	var enable, disable bool
	cmd := &cobra.Command{
		Use:     "skip-auto-san <domain-name|domain-id>",
		Short:   "Opt a domain in/out of the panel-cert auto-SAN (M50)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable == disable {
				return fmt.Errorf("pass exactly one of --enable / --disable")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			if err := domainRepoFromDB().UpdateSkipAutoSAN(ctx, d.ID, enable); err != nil {
				return fmt.Errorf("update skip_auto_san: %w", err)
			}
			cliAuditOK(ctx, "domain.skip_auto_san", "domain", d.ID, &d.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "%s skip-auto-SAN=%v. Converges on the next reconcile pass.\n", d.Name, enable)
			return nil
		},
	}
	cmd.Flags().BoolVar(&enable, "enable", false, "exclude this domain from the panel cert SAN")
	cmd.Flags().BoolVar(&disable, "disable", false, "include this domain in the panel cert SAN")
	return cmd
}

func newDomainMTAStsCmd() *cobra.Command {
	var enable, disable bool
	cmd := &cobra.Command{
		Use:     "mta-sts <domain-name|domain-id>",
		Short:   "Enable/disable MTA-STS for a mail domain (ADR-0109)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable == disable {
				return fmt.Errorf("pass exactly one of --enable / --disable")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			if _, err := domainRepoFromDB().UpdateMTASTSEnabled(ctx, d.ID, enable); err != nil {
				return fmt.Errorf("update mta_sts_enabled: %w", err)
			}
			cliAuditOK(ctx, "domain.mta_sts", "domain", d.ID, &d.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "%s MTA-STS=%v. The reconciler publishes/removes the DNS + policy on the next pass.\n", d.Name, enable)
			return nil
		},
	}
	cmd.Flags().BoolVar(&enable, "enable", false, "enable MTA-STS")
	cmd.Flags().BoolVar(&disable, "disable", false, "disable MTA-STS")
	return cmd
}
