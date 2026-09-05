package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/packageops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Hosting packages are pure DB rows — no agent side-effect, no Kratos hook.
// Under M20 the CLI goes direct-DB so these commands stay usable even after
// the legacy JWT middleware is gone.

func newPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Manage hosting packages",
	}
	cmd.AddCommand(
		newPackageListCmd(),
		newPackageCreateCmd(),
		newPackageEditCmd(),
		newPackageDeleteCmd(),
	)
	return cmd
}

func newPackageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List hosting packages (direct DB — M20-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			pkgs, _, err := packageRepoFromDB().List(ctx, repository.ListOptions{Limit: 1000})
			if err != nil {
				return fmt.Errorf("list packages: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]interface{}{
					"packages": pkgs,
					"total":    len(pkgs),
				})
			}
			if len(pkgs) == 0 {
				fmt.Println("No packages found")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tDISK_MB\tBW_MB\tDOMAINS\tDBS\tSSH\tCGI")
			for _, p := range pkgs {
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n",
					p.ID, p.Name, p.DiskQuotaMB, p.BandwidthQuotaMB,
					p.MaxDomains, p.MaxDatabases,
					boolYN(p.SSHEnabled), boolYN(p.CGIEnabled))
			}
			return w.Flush()
		},
	}
}

func newPackageCreateCmd() *cobra.Command {
	var (
		name        string
		diskMB      uint32
		bwMB        uint32
		domains     uint32
		emails      uint32
		databases   uint32
		cpuPercent  uint32
		memoryMB    uint32
		ioReadMbps  uint32
		ioWriteMbps uint32
		maxTasks    uint32
		sshEnabled  bool
		cgiEnabled  bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a hosting package (direct DB — M20-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			now := time.Now().UTC()
			p := &models.HostingPackage{
				ID:               ids.NewULID(),
				Name:             name,
				DiskQuotaMB:      diskMB,
				BandwidthQuotaMB: bwMB,
				MaxDomains:       domains,
				MaxEmailAccounts: emails,
				MaxDatabases:     databases,
				CPUQuotaPercent:  cpuPercent,
				MemoryLimitMB:    memoryMB,
				IOReadMbps:       ioReadMbps,
				IOWriteMbps:      ioWriteMbps,
				MaxTasks:         maxTasks,
				SSHEnabled:       sshEnabled,
				CGIEnabled:       cgiEnabled,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			// JAB-306: the operator CLI persists direct-to-DB, so it must run
			// the same invariant check the REST handler does — otherwise an
			// out-of-range limit the API rejects with 422 (e.g. --cpu 999999)
			// would silently land in the row. Unset FPM/backup fields fall to
			// their GORM column defaults at INSERT and pass.
			if err := packageops.Validate(p); err != nil {
				return fmt.Errorf("invalid package: %w", err)
			}
			if err := packageRepoFromDB().Create(ctx, p); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					return fmt.Errorf("package name %q already exists", name)
				}
				return fmt.Errorf("create package: %w", err)
			}
			if jsonOutput {
				return printJSON(p)
			}
			cliAuditOK(ctx, "package.create", "package", p.ID, nil)
			fmt.Printf("Created package %s (%s)\n", p.ID, p.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "package name (required)")
	cmd.Flags().Uint32Var(&diskMB, "disk-mb", 0, "disk quota in MB (0=unlimited)")
	cmd.Flags().Uint32Var(&bwMB, "bw-mb", 0, "bandwidth quota in MB (0=unlimited)")
	cmd.Flags().Uint32Var(&domains, "domains", 0, "max domains (0=unlimited)")
	cmd.Flags().Uint32Var(&emails, "emails", 0, "max email accounts (0=unlimited)")
	cmd.Flags().Uint32Var(&databases, "databases", 0, "max databases (0=unlimited)")
	cmd.Flags().Uint32Var(&cpuPercent, "cpu", 0, "CPU quota percent across all cores (0=unlimited)")
	cmd.Flags().Uint32Var(&memoryMB, "memory-mb", 0, "memory limit in MB (0=unlimited)")
	cmd.Flags().Uint32Var(&ioReadMbps, "io-read-mbps", 0, "disk read bandwidth limit in MB/s (0=unlimited)")
	cmd.Flags().Uint32Var(&ioWriteMbps, "io-write-mbps", 0, "disk write bandwidth limit in MB/s (0=unlimited)")
	cmd.Flags().Uint32Var(&maxTasks, "max-tasks", 0, "max processes/threads per user slice (0=unlimited)")
	cmd.Flags().BoolVar(&sshEnabled, "ssh", false, "enable SSH access")
	cmd.Flags().BoolVar(&cgiEnabled, "cgi", false, "enable CGI")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newPackageEditCmd() *cobra.Command {
	var (
		name        string
		diskMB      uint32
		bwMB        uint32
		domains     uint32
		emails      uint32
		databases   uint32
		cpuPercent  uint32
		memoryMB    uint32
		ioReadMbps  uint32
		ioWriteMbps uint32
		maxTasks    uint32
		sshEnabled  string
		cgiEnabled  string
	)

	cmd := &cobra.Command{
		Use:   "edit <package-id>",
		Short: "Edit a hosting package (direct DB — M20-safe)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}

			repo := packageRepoFromDB()
			p, err := resolvePackage(ctx, repo, args[0])
			if err != nil {
				return err
			}

			changed := false
			if cmd.Flags().Changed("name") {
				p.Name = name
				changed = true
			}
			if cmd.Flags().Changed("disk-mb") {
				p.DiskQuotaMB = diskMB
				changed = true
			}
			if cmd.Flags().Changed("bw-mb") {
				p.BandwidthQuotaMB = bwMB
				changed = true
			}
			if cmd.Flags().Changed("domains") {
				p.MaxDomains = domains
				changed = true
			}
			if cmd.Flags().Changed("emails") {
				p.MaxEmailAccounts = emails
				changed = true
			}
			if cmd.Flags().Changed("databases") {
				p.MaxDatabases = databases
				changed = true
			}
			if cmd.Flags().Changed("cpu") {
				p.CPUQuotaPercent = cpuPercent
				changed = true
			}
			if cmd.Flags().Changed("memory-mb") {
				p.MemoryLimitMB = memoryMB
				changed = true
			}
			if cmd.Flags().Changed("io-read-mbps") {
				p.IOReadMbps = ioReadMbps
				changed = true
			}
			if cmd.Flags().Changed("io-write-mbps") {
				p.IOWriteMbps = ioWriteMbps
				changed = true
			}
			if cmd.Flags().Changed("max-tasks") {
				p.MaxTasks = maxTasks
				changed = true
			}
			if sshEnabled == "true" {
				p.SSHEnabled = true
				changed = true
			} else if sshEnabled == "false" {
				p.SSHEnabled = false
				changed = true
			}
			if cgiEnabled == "true" {
				p.CGIEnabled = true
				changed = true
			} else if cgiEnabled == "false" {
				p.CGIEnabled = false
				changed = true
			}
			if !changed {
				return fmt.Errorf("no changes specified")
			}
			p.UpdatedAt = time.Now().UTC()
			// JAB-306: same invariant gate as create + the REST handler, so an
			// edit can't drive a limit out of the bounds the API enforces. The
			// loaded row already carries valid FPM/backup values, so this only
			// rejects an operator's out-of-range edit — never "heals" untouched
			// fields.
			if err := packageops.Validate(p); err != nil {
				return fmt.Errorf("invalid package: %w", err)
			}
			if err := repo.Update(ctx, p); err != nil {
				return fmt.Errorf("update package: %w", err)
			}
			if jsonOutput {
				return printJSON(p)
			}
			cliAuditOK(ctx, "package.update", "package", p.ID, nil)
			fmt.Printf("Updated package %s (%s)\n", p.ID, p.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "package name")
	cmd.Flags().Uint32Var(&diskMB, "disk-mb", 0, "disk quota MB")
	cmd.Flags().Uint32Var(&bwMB, "bw-mb", 0, "bandwidth MB")
	cmd.Flags().Uint32Var(&domains, "domains", 0, "max domains")
	cmd.Flags().Uint32Var(&emails, "emails", 0, "max emails")
	cmd.Flags().Uint32Var(&databases, "databases", 0, "max databases")
	cmd.Flags().Uint32Var(&cpuPercent, "cpu", 0, "CPU quota percent")
	cmd.Flags().Uint32Var(&memoryMB, "memory-mb", 0, "memory limit MB")
	cmd.Flags().Uint32Var(&ioReadMbps, "io-read-mbps", 0, "io read MB/s")
	cmd.Flags().Uint32Var(&ioWriteMbps, "io-write-mbps", 0, "io write MB/s")
	cmd.Flags().Uint32Var(&maxTasks, "max-tasks", 0, "max processes/threads")
	cmd.Flags().StringVar(&sshEnabled, "ssh", "", "SSH access (true/false)")
	cmd.Flags().StringVar(&cgiEnabled, "cgi", "", "CGI access (true/false)")
	return cmd
}

func newPackageDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <package-id>",
		Short: "Delete a hosting package (direct DB — M20-safe)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			repo := packageRepoFromDB()
			p, err := resolvePackage(ctx, repo, args[0])
			if err != nil {
				return err
			}
			if !force {
				fmt.Printf("Delete package %s (%s)? [y/N]: ", p.ID, p.Name)
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			if err := repo.Delete(ctx, p.ID); err != nil {
				return fmt.Errorf("delete package: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]string{"deleted": p.ID})
			}
			cliAuditOK(ctx, "package.delete", "package", p.ID, nil)
			fmt.Printf("Deleted package %s (%s)\n", p.ID, p.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

// resolvePackage accepts either a package ULID or its name (operators
// remember names; ULIDs only useful when scripting). Tries ID first
// (cheap exact-match lookup); falls back to name. Mirrors resolveUser
// but unique to packages because hosting_packages has both columns
// indexed.
func resolvePackage(ctx context.Context, repo repository.PackageRepository, lookup string) (*models.HostingPackage, error) {
	if p, err := repo.FindByID(ctx, lookup); err == nil {
		return p, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("lookup by id: %w", err)
	}
	if p, err := repo.FindByName(ctx, lookup); err == nil {
		return p, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("lookup by name: %w", err)
	}
	return nil, fmt.Errorf("package %q not found", lookup)
}

func boolYN(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
