// Admin IP address manager CLI (Gitea #555). Mirrors /admin/ips (ips.go):
// list/add/update/delete managed IP pool rows with the same validation
// (DeriveFamily + routable check), uniqueness, default-per-family promotion,
// and in-use protection. The DB is the source of truth; the agent binds rows to
// the kernel on its managed-ips sync (the API's push-bind is feature-flagged and
// not replicated here). Mutations are CLI-audited; inventory honours --json.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

func ipRepoFromDB() repository.ManagedIPRepository {
	return repository.NewManagedIPRepository(sharedDB)
}

func validateRoutableIPCLI(addr string) error {
	ip := net.ParseIP(addr)
	if ip == nil {
		return errors.New("not a valid IP")
	}
	switch {
	case ip.IsLoopback():
		return errors.New("loopback addresses cannot be in the IP pool")
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return errors.New("link-local addresses cannot be in the IP pool")
	case ip.IsMulticast():
		return errors.New("multicast addresses cannot be in the IP pool")
	case ip.IsUnspecified():
		return errors.New("unspecified address cannot be in the IP pool")
	}
	return nil
}

func newIPManagerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ip",
		Aliases: []string{"ips"},
		Short:   "Admin IP address pool (managed_ips): list / add / update / delete",
	}
	cmd.AddCommand(newIPListCmd(), newIPAddCmd(), newIPUpdateCmd(), newIPDeleteCmd())
	return cmd
}

func newIPListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List managed IP addresses",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			rows, err := ipRepoFromDB().ListAll(ctx)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tADDRESS\tFAMILY\tDEFAULT\tUSER_SELECTABLE\tLABEL")
			for _, r := range rows {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%v\t%v\t%s\n", r.ID, r.Address, r.Family, r.IsDefault, r.IsUserSelectable, r.Label)
			}
			return tw.Flush()
		},
	}
}

func newIPAddCmd() *cobra.Command {
	var label string
	var userSelectable bool
	cmd := &cobra.Command{
		Use:     "add <address>",
		Short:   "Add an IP to the managed pool",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			addr := args[0]
			family := models.DeriveFamily(addr)
			if family == "" {
				return fmt.Errorf("%q is not a valid IPv4 or IPv6 address", addr)
			}
			if err := validateRoutableIPCLI(addr); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			row := &models.ManagedIP{Address: addr, Family: family, Label: label, IsUserSelectable: userSelectable}
			if err := ipRepoFromDB().Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					return fmt.Errorf("address %s is already in the pool", addr)
				}
				return fmt.Errorf("create IP: %w", err)
			}
			cliAuditOK(ctx, "managed_ip.add", "managed_ip", strconv.FormatUint(row.ID, 10), nil)
			if jsonOutput {
				return printJSON(row)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s (%s) id=%d. The agent binds it to the kernel on its next managed-ips sync.\n", addr, family, row.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "human label")
	cmd.Flags().BoolVar(&userSelectable, "user-selectable", false, "tenants may pick this IP")
	return cmd
}

func newIPUpdateCmd() *cobra.Command {
	var label string
	var userSelectable, makeDefault bool
	cmd := &cobra.Command{
		Use:     "update <id>",
		Short:   "Update an IP's label / user-selectable / default flag",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("id must be a positive integer")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := ipRepoFromDB()
			row, err := repo.FindByID(ctx, id)
			if err != nil {
				return fmt.Errorf("IP not found: %w", err)
			}
			if cmd.Flags().Changed("label") {
				row.Label = label
			}
			if cmd.Flags().Changed("user-selectable") {
				row.IsUserSelectable = userSelectable
			}
			promoted := false
			if cmd.Flags().Changed("default") {
				if !makeDefault {
					return fmt.Errorf("cannot un-set default; promote another IP to default instead")
				}
				if !row.IsDefault {
					// Demote the existing per-family default first.
					if cur, derr := repo.FindDefaultByFamily(ctx, row.Family); derr == nil && cur != nil && cur.ID != row.ID {
						cur.IsDefault = false
						if uerr := repo.Update(ctx, cur); uerr != nil {
							return fmt.Errorf("demote existing default: %w", uerr)
						}
					}
					row.IsDefault = true
					promoted = true
				}
			}
			if err := repo.Update(ctx, row); err != nil {
				return fmt.Errorf("update IP: %w", err)
			}
			cliAuditOK(ctx, "managed_ip.update", "managed_ip", args[0], nil)
			if promoted {
				if err := followDefaultIPv4(ctx, row); err != nil {
					return err
				}
			}
			if jsonOutput {
				return printJSON(row)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated IP %d\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "new label")
	cmd.Flags().BoolVar(&userSelectable, "user-selectable", false, "tenants may pick this IP")
	cmd.Flags().BoolVar(&makeDefault, "default", false, "promote to per-family default")
	return cmd
}

// followDefaultIPv4 moves what install.sh pinned to the old address (the
// panel's default server, /etc/hosts, the CrowdSec self-whitelist) to a newly
// promoted default IPv4, the same call the REST PATCH makes; tenant vhosts
// follow on the next reconcile pass. An IPv6 default has nothing to move.
func followDefaultIPv4(ctx context.Context, row *models.ManagedIP) error {
	if row.Family != "ipv4" {
		return nil
	}
	call := settingsops.DefaultIPv4RehostCall(row.Address)
	err := initAgent()
	if err == nil {
		err = dispatchSettingsAgentCall(ctx, call)
	}
	if err != nil {
		return fmt.Errorf("IP %d (%s) is the default now, but the server's own settings did not follow it (%s): %w", row.ID, row.Address, call.Method, err)
	}
	return nil
}

func newIPDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete an IP from the pool",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("id must be a positive integer")
			}
			if !force {
				return fmt.Errorf("re-run with --force to delete an IP from the pool")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := ipRepoFromDB()
			row, err := repo.FindByID(ctx, id)
			if err != nil {
				return fmt.Errorf("IP not found: %w", err)
			}
			if row.IsDefault {
				return fmt.Errorf("cannot delete the %s default IP; promote another to default first", row.Family)
			}
			count, cerr := repo.CountDomainsUsingIP(ctx, id)
			if cerr != nil {
				return fmt.Errorf("check usage: %w", cerr)
			}
			if count > 0 {
				return fmt.Errorf("IP is bound to %d domain(s); reassign them before deleting", count)
			}
			if err := repo.Delete(ctx, id); err != nil {
				return fmt.Errorf("delete IP: %w", err)
			}
			cliAuditOK(ctx, "managed_ip.delete", "managed_ip", args[0], nil)
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted IP %d (%s)\n", id, row.Address)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}
