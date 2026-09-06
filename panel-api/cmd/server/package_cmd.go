package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

// packageCreateFlags collects every `package create` flag so the row build is a
// pure function of the flag values — testable without a live DB. Bool flags are
// plain booleans here (create has no "leave unchanged" state; absence = false,
// same as the REST create request's zero value).
type packageCreateFlags struct {
	name string

	diskMB, bwMB, domains, emails, databases      uint32
	cpuPercent, memoryMB, ioReadMbps, ioWriteMbps uint32
	maxTasks                                      uint32

	maxDBUsers, maxDockerApps, maxPythonApps, maxFTP uint32

	maxBackups, maxBackupSchedules uint32
	scheduledBackups               bool
	backupKinds, backupRetention   string

	sshEnabled, cgiEnabled, phpExec bool

	fpmMaxChildren, fpmWorkerMemMB uint32
	fpmUserCanEdit, fpmAdvanced    bool
	fpmVersionDefaults             string

	dockerAppSlugs string
	nspawnImage    string
}

// buildPackageFromCreateFlags maps create-flag values onto a hosting_packages
// row and applies the SAME defaulting the REST create handler does
// (internal/api/packages.go:166-242), so an equivalent CLI invocation persists
// the identical row. We apply the FPM/retention/version defaults explicitly
// rather than leaning on the GORM `default:` column tags (which only fill a
// field left at its zero value) so "equivalent inputs → identical rows" is
// provable by reading the two blocks side by side. packageops.Validate is the
// shared final gate the caller runs on the returned row.
func buildPackageFromCreateFlags(f packageCreateFlags) (*models.HostingPackage, error) {
	now := time.Now().UTC()
	p := &models.HostingPackage{
		ID:               ids.NewULID(),
		Name:             f.name,
		DiskQuotaMB:      f.diskMB,
		BandwidthQuotaMB: f.bwMB,
		MaxDomains:       f.domains,
		MaxEmailAccounts: f.emails,
		MaxDatabases:     f.databases,
		CPUQuotaPercent:  f.cpuPercent,
		MemoryLimitMB:    f.memoryMB,
		IOReadMbps:       f.ioReadMbps,
		IOWriteMbps:      f.ioWriteMbps,
		MaxTasks:         f.maxTasks,
		MaxDatabaseUsers: f.maxDBUsers,
		MaxDockerApps:    f.maxDockerApps,
		MaxPythonApps:    f.maxPythonApps,
		MaxFTPAccounts:   f.maxFTP,

		MaxBackups:                    f.maxBackups,
		MaxBackupSchedules:            f.maxBackupSchedules,
		ScheduledBackupsEnabled:       f.scheduledBackups,
		AllowedBackupDestinationKinds: f.backupKinds,
		BackupRetentionPolicy:         f.backupRetention,

		SSHEnabled:         f.sshEnabled,
		CGIEnabled:         f.cgiEnabled,
		PHPExecEnabled:     f.phpExec,
		FpmMaxChildrenCap:  f.fpmMaxChildren,
		FpmWorkerMemMb:     f.fpmWorkerMemMB,
		FpmUserCanEdit:     f.fpmUserCanEdit,
		FpmAdvancedMode:    f.fpmAdvanced,
		FpmVersionDefaults: f.fpmVersionDefaults,
		DockerAppSlugs:     f.dockerAppSlugs,

		CreatedAt: now,
		UpdatedAt: now,
	}
	// FPM policy defaults (mirror packages.go:166-182).
	if p.FpmMaxChildrenCap == 0 {
		p.FpmMaxChildrenCap = 20
	}
	if p.FpmWorkerMemMb == 0 {
		p.FpmWorkerMemMb = 64
	}
	if p.FpmVersionDefaults == "" {
		p.FpmVersionDefaults = "{}"
	}
	if p.FpmAdvancedMode {
		p.FpmUserCanEdit = true // advanced implies can-edit
	}
	// Backup destination kinds: canonicalise (dedup/lowercase) so a CLI-supplied
	// list stores byte-for-byte what the REST path would (packages.go:183-189).
	normKinds, err := models.NormalizeBackupKindsCSV(p.AllowedBackupDestinationKinds)
	if err != nil {
		return nil, fmt.Errorf("invalid backup kinds: %w", err)
	}
	p.AllowedBackupDestinationKinds = normKinds
	// Retention policy: empty → the safe "reject" default (packages.go:190-196).
	if p.BackupRetentionPolicy == "" {
		p.BackupRetentionPolicy = models.BackupRetentionReject
	}
	// nspawn image: empty → server default (NULL). A non-empty value is pinned
	// and pattern-checked by packageops.Validate.
	if v := strings.TrimSpace(f.nspawnImage); v != "" {
		p.NspawnImageVersion = &v
	}
	return p, nil
}

func newPackageCreateCmd() *cobra.Command {
	var f packageCreateFlags

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
			p, err := buildPackageFromCreateFlags(f)
			if err != nil {
				return err
			}
			// JAB-306: the operator CLI persists direct-to-DB, so it must run the
			// same invariant check the REST handler does — otherwise an
			// out-of-range value the API rejects with 422 (e.g. --cpu 999999 or
			// --fpm-max-children 3000) would silently land in the row.
			if err := packageops.Validate(p); err != nil {
				return fmt.Errorf("invalid package: %w", err)
			}
			if err := packageRepoFromDB().Create(ctx, p); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					return fmt.Errorf("package name %q already exists", f.name)
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

	registerPackageCreateFlags(cmd, &f)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// registerPackageCreateFlags wires every hosting_packages entitlement onto the
// create command. Every field of the REST create request has a flag here — the
// JAB-306 AC3 parity guarantee ("new entitlement fields cannot be omitted by one
// adapter"), enforced by TestPackageCLI_EntitlementFlagParity.
func registerPackageCreateFlags(cmd *cobra.Command, f *packageCreateFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "package name (required)")
	fl.Uint32Var(&f.diskMB, "disk-mb", 0, "disk quota in MB (0=unlimited)")
	fl.Uint32Var(&f.bwMB, "bw-mb", 0, "bandwidth quota in MB (0=unlimited)")
	fl.Uint32Var(&f.domains, "domains", 0, "max domains (0=unlimited)")
	fl.Uint32Var(&f.emails, "emails", 0, "max email accounts (0=unlimited)")
	fl.Uint32Var(&f.databases, "databases", 0, "max databases (0=unlimited)")
	fl.Uint32Var(&f.cpuPercent, "cpu", 0, "CPU quota percent across all cores (0=unlimited)")
	fl.Uint32Var(&f.memoryMB, "memory-mb", 0, "memory limit in MB (0=unlimited)")
	fl.Uint32Var(&f.ioReadMbps, "io-read-mbps", 0, "disk read bandwidth limit in MB/s (0=unlimited)")
	fl.Uint32Var(&f.ioWriteMbps, "io-write-mbps", 0, "disk write bandwidth limit in MB/s (0=unlimited)")
	fl.Uint32Var(&f.maxTasks, "max-tasks", 0, "max processes/threads per user slice (0=unlimited)")
	fl.Uint32Var(&f.maxDBUsers, "max-db-users", 0, "max database users (0=unlimited)")
	fl.Uint32Var(&f.maxDockerApps, "max-docker-apps", 0, "max tenant docker apps (0=docker apps not included)")
	fl.Uint32Var(&f.maxPythonApps, "max-python-apps", 0, "max tenant python apps (0=python apps not included)")
	fl.Uint32Var(&f.maxFTP, "max-ftp-accounts", 0, "max tenant FTP/SFTP subaccounts (0=feature not included)")
	fl.Uint32Var(&f.maxBackups, "max-backups", 0, "tenant backup retention cap (0=backups not included)")
	fl.Uint32Var(&f.maxBackupSchedules, "max-backup-schedules", 0, "max tenant scheduled backups (0=default 1)")
	fl.BoolVar(&f.scheduledBackups, "scheduled-backups", false, "allow tenant scheduled backups")
	fl.StringVar(&f.backupKinds, "backup-kinds", "", "CSV of allowed backup destination kinds (empty=none)")
	fl.StringVar(&f.backupRetention, "backup-retention", "", "retention policy at the cap: reject or prune (empty=reject)")
	fl.BoolVar(&f.sshEnabled, "ssh", false, "enable SSH access")
	fl.BoolVar(&f.cgiEnabled, "cgi", false, "enable CGI")
	fl.BoolVar(&f.phpExec, "php-exec", false, "opt out of the PHP command-exec lockdown (exec/proc_open work)")
	fl.Uint32Var(&f.fpmMaxChildren, "fpm-max-children", 0, "FPM pm.max_children cap (0=default 20)")
	fl.Uint32Var(&f.fpmWorkerMemMB, "fpm-worker-mem-mb", 0, "FPM advisory per-worker memory budget in MB (0=default 64)")
	fl.BoolVar(&f.fpmUserCanEdit, "fpm-user-can-edit", false, "let tenants pick an FPM performance mode")
	fl.BoolVar(&f.fpmAdvanced, "fpm-advanced", false, "unlock tenant advanced FPM knobs (implies --fpm-user-can-edit)")
	fl.StringVar(&f.fpmVersionDefaults, "fpm-version-defaults", "", `JSON map of per-version FPM defaults (empty={})`)
	fl.StringVar(&f.dockerAppSlugs, "docker-app-slugs", "", "CSV allowlist of catalog slugs tenants may install (empty=server default)")
	fl.StringVar(&f.nspawnImage, "nspawn-image", "", "pin users to an nspawn rootfs version (empty=server default)")
}

// packageEditFlags mirrors packageCreateFlags for the edit command. The six
// boolean entitlements are tri-state strings ("" leave unchanged, "true",
// "false") so an edit only touches a toggle the operator names — the same
// convention the original --ssh/--cgi edit flags use.
type packageEditFlags struct {
	name string

	diskMB, bwMB, domains, emails, databases      uint32
	cpuPercent, memoryMB, ioReadMbps, ioWriteMbps uint32
	maxTasks                                      uint32

	maxDBUsers, maxDockerApps, maxPythonApps, maxFTP uint32

	maxBackups, maxBackupSchedules uint32
	backupKinds, backupRetention   string

	fpmMaxChildren, fpmWorkerMemMB                  uint32
	fpmVersionDefaults, dockerAppSlugs, nspawnImage string

	// tri-state toggles.
	sshEnabled, cgiEnabled, phpExec               string
	scheduledBackups, fpmUserCanEdit, fpmAdvanced string
}

// applyPackageEditFlags applies the named edit flags onto a loaded row and
// reports whether anything changed. `changed` is cmd.Flags().Changed (passed in
// so the builder is a pure function testable without cobra plumbing). Every
// per-field copy is gated on the operator naming that flag, mirroring the REST
// update handler (internal/api/packages.go:296-419) — an omitted flag never
// heals an untouched field. Backup-kinds normalisation, retention defaulting,
// nspawn clear-on-empty and the unconditional advanced→can-edit implication all
// match the REST update path. packageops.Validate is the shared final gate the
// caller runs afterwards.
func applyPackageEditFlags(changed func(string) bool, p *models.HostingPackage, f packageEditFlags) (bool, error) {
	dirty := false
	u32 := func(flag string, dst *uint32, v uint32) {
		if changed(flag) {
			*dst = v
			dirty = true
		}
	}
	str := func(flag string, dst *string, v string) {
		if changed(flag) {
			*dst = v
			dirty = true
		}
	}
	var triErr error
	tri := func(flag string, dst *bool, v string) {
		switch v {
		case "":
			// unset — leave the toggle unchanged.
		case "true":
			*dst = true
			dirty = true
		case "false":
			*dst = false
			dirty = true
		default:
			// Fail loud on a garbage value ("--php-exec=yes") instead of silently
			// ignoring it — a silently-dropped security toggle is exactly the
			// reported-success-that-does-nothing trap (feedback_silent_exit0_failures).
			if triErr == nil {
				triErr = fmt.Errorf("--%s: want true or false, got %q", flag, v)
			}
		}
	}

	str("name", &p.Name, f.name)
	u32("disk-mb", &p.DiskQuotaMB, f.diskMB)
	u32("bw-mb", &p.BandwidthQuotaMB, f.bwMB)
	u32("domains", &p.MaxDomains, f.domains)
	u32("emails", &p.MaxEmailAccounts, f.emails)
	u32("databases", &p.MaxDatabases, f.databases)
	u32("cpu", &p.CPUQuotaPercent, f.cpuPercent)
	u32("memory-mb", &p.MemoryLimitMB, f.memoryMB)
	u32("io-read-mbps", &p.IOReadMbps, f.ioReadMbps)
	u32("io-write-mbps", &p.IOWriteMbps, f.ioWriteMbps)
	u32("max-tasks", &p.MaxTasks, f.maxTasks)
	u32("max-db-users", &p.MaxDatabaseUsers, f.maxDBUsers)
	u32("max-docker-apps", &p.MaxDockerApps, f.maxDockerApps)
	u32("max-python-apps", &p.MaxPythonApps, f.maxPythonApps)
	u32("max-ftp-accounts", &p.MaxFTPAccounts, f.maxFTP)
	u32("max-backups", &p.MaxBackups, f.maxBackups)
	u32("max-backup-schedules", &p.MaxBackupSchedules, f.maxBackupSchedules)
	u32("fpm-max-children", &p.FpmMaxChildrenCap, f.fpmMaxChildren)
	u32("fpm-worker-mem-mb", &p.FpmWorkerMemMb, f.fpmWorkerMemMB)
	str("fpm-version-defaults", &p.FpmVersionDefaults, f.fpmVersionDefaults)
	str("docker-app-slugs", &p.DockerAppSlugs, f.dockerAppSlugs)

	tri("ssh", &p.SSHEnabled, f.sshEnabled)
	tri("cgi", &p.CGIEnabled, f.cgiEnabled)
	tri("php-exec", &p.PHPExecEnabled, f.phpExec)
	tri("scheduled-backups", &p.ScheduledBackupsEnabled, f.scheduledBackups)
	tri("fpm-user-can-edit", &p.FpmUserCanEdit, f.fpmUserCanEdit)
	tri("fpm-advanced", &p.FpmAdvancedMode, f.fpmAdvanced)
	if triErr != nil {
		return false, triErr
	}

	if changed("backup-kinds") {
		nk, err := models.NormalizeBackupKindsCSV(f.backupKinds)
		if err != nil {
			return false, fmt.Errorf("invalid backup kinds: %w", err)
		}
		p.AllowedBackupDestinationKinds = nk
		dirty = true
	}
	if changed("backup-retention") {
		v := f.backupRetention
		if v == "" {
			v = models.BackupRetentionReject
		}
		p.BackupRetentionPolicy = v
		dirty = true
	}
	if changed("nspawn-image") {
		if v := strings.TrimSpace(f.nspawnImage); v == "" {
			p.NspawnImageVersion = nil // empty clears the pin (server default)
		} else {
			p.NspawnImageVersion = &v
		}
		dirty = true
	}
	// Advanced implies can-edit — unconditional, matching packages.go:399.
	if p.FpmAdvancedMode {
		p.FpmUserCanEdit = true
	}
	return dirty, nil
}

func newPackageEditCmd() *cobra.Command {
	var f packageEditFlags

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

			didChange, err := applyPackageEditFlags(cmd.Flags().Changed, p, f)
			if err != nil {
				return err
			}
			if !didChange {
				return fmt.Errorf("no changes specified")
			}
			p.UpdatedAt = time.Now().UTC()
			// JAB-306: same invariant gate as create + the REST handler, so an
			// edit can't drive a value out of the bounds the API enforces. The
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

	registerPackageEditFlags(cmd, &f)
	return cmd
}

// registerPackageEditFlags wires the entitlement flags onto the edit command.
// The uint32/string flags share the create names; the six boolean toggles are
// tri-state strings (true/false) so an edit only flips a toggle the operator
// names. Parity with the REST update request is enforced by
// TestPackageCLI_EntitlementFlagParity.
func registerPackageEditFlags(cmd *cobra.Command, f *packageEditFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "package name")
	fl.Uint32Var(&f.diskMB, "disk-mb", 0, "disk quota MB")
	fl.Uint32Var(&f.bwMB, "bw-mb", 0, "bandwidth MB")
	fl.Uint32Var(&f.domains, "domains", 0, "max domains")
	fl.Uint32Var(&f.emails, "emails", 0, "max emails")
	fl.Uint32Var(&f.databases, "databases", 0, "max databases")
	fl.Uint32Var(&f.cpuPercent, "cpu", 0, "CPU quota percent")
	fl.Uint32Var(&f.memoryMB, "memory-mb", 0, "memory limit MB")
	fl.Uint32Var(&f.ioReadMbps, "io-read-mbps", 0, "io read MB/s")
	fl.Uint32Var(&f.ioWriteMbps, "io-write-mbps", 0, "io write MB/s")
	fl.Uint32Var(&f.maxTasks, "max-tasks", 0, "max processes/threads")
	fl.Uint32Var(&f.maxDBUsers, "max-db-users", 0, "max database users")
	fl.Uint32Var(&f.maxDockerApps, "max-docker-apps", 0, "max tenant docker apps")
	fl.Uint32Var(&f.maxPythonApps, "max-python-apps", 0, "max tenant python apps")
	fl.Uint32Var(&f.maxFTP, "max-ftp-accounts", 0, "max tenant FTP/SFTP subaccounts")
	fl.Uint32Var(&f.maxBackups, "max-backups", 0, "tenant backup retention cap")
	fl.Uint32Var(&f.maxBackupSchedules, "max-backup-schedules", 0, "max tenant scheduled backups")
	fl.StringVar(&f.backupKinds, "backup-kinds", "", "CSV of allowed backup destination kinds")
	fl.StringVar(&f.backupRetention, "backup-retention", "", "retention policy at the cap: reject or prune")
	fl.Uint32Var(&f.fpmMaxChildren, "fpm-max-children", 0, "FPM pm.max_children cap")
	fl.Uint32Var(&f.fpmWorkerMemMB, "fpm-worker-mem-mb", 0, "FPM advisory per-worker memory budget MB")
	fl.StringVar(&f.fpmVersionDefaults, "fpm-version-defaults", "", "JSON map of per-version FPM defaults")
	fl.StringVar(&f.dockerAppSlugs, "docker-app-slugs", "", "CSV allowlist of catalog slugs tenants may install")
	fl.StringVar(&f.nspawnImage, "nspawn-image", "", "pin users to an nspawn rootfs version (empty clears the pin)")
	fl.StringVar(&f.sshEnabled, "ssh", "", "SSH access (true/false)")
	fl.StringVar(&f.cgiEnabled, "cgi", "", "CGI access (true/false)")
	fl.StringVar(&f.phpExec, "php-exec", "", "PHP command-exec opt-out (true/false)")
	fl.StringVar(&f.scheduledBackups, "scheduled-backups", "", "tenant scheduled backups (true/false)")
	fl.StringVar(&f.fpmUserCanEdit, "fpm-user-can-edit", "", "tenant FPM performance mode (true/false)")
	fl.StringVar(&f.fpmAdvanced, "fpm-advanced", "", "tenant advanced FPM knobs (true/false, true implies fpm-user-can-edit)")
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
