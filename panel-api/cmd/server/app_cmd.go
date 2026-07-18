package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
)

// `jabali app *` is a thin direct-DB CLI for the M19 application
// installer surface — mirrors the user/domain pattern in cli_ops.go
// rather than going through HTTP (which 401s without a Kratos session cookie).
//
// Scope deliberately omits `create` and `e2e`: the HTTP create handler
// in api.applications.go pulls in 16 per-app kicker goroutines and
// provisionDBChain — all package-private. Adding a CLI create requires
// extracting that pipeline into a shared service package; tracked
// separately. Operators can still install apps from the UI.
func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage one-click app installs (direct DB — M20-safe)",
	}
	cmd.AddCommand(
		newAppRegistryCmd(),
		newAppListCmd(),
		newAppGetCmd(),
		newAppInstallCmd(),
		newAppScanCmd(),
		newAppCacheCmd(),
		newAppRefreshCachePluginCmd(),
		newAppCacheDoctorCmd(),
		newAppCacheDoctorRunCmd(),
		newAppCloneCmd(),
		newAppMagicLinkCmd(),
		newAppDeleteCmd(),
		newAppE2ECmd(),
	)
	return cmd
}

// ---- registry ----

func newAppRegistryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "registry",
		Short: "List available app types and their parameter schemas (direct read — no DB)",
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := listAppRegistry()
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]interface{}{
					"apps":  entries,
					"total": len(entries),
				})
			}

			if len(entries) == 0 {
				fmt.Println("No apps registered")
				return nil
			}

			// Sort by name so the table is stable across runs (registry
			// iteration order is map-random).
			sorted := append([]apps.App(nil), entries...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDISPLAY\tDB\tPARAMS")
			for _, e := range sorted {
				dbCol := "-"
				if e.RequiresDB {
					dbCol = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, e.DisplayName, dbCol, paramSummary(e.InstallParamSchema))
			}
			return w.Flush()
		},
	}
}

// paramSummary renders the param schema as `name(type[!])` joined by
// ", " so the registry table fits on one line. `!` flags required.
// Missing schemas (RequiresDB-less apps) are rendered as "-" rather
// than blank for readability.
func paramSummary(schema map[string]apps.ParamSpec) string {
	if len(schema) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		spec := schema[k]
		flag := ""
		if spec.Required {
			flag = "!"
		}
		parts = append(parts, fmt.Sprintf("%s(%s%s)", k, spec.Type, flag))
	}
	return strings.Join(parts, ", ")
}

// ---- list ----

func newAppListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed apps (direct DB — M20-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			installs, err := listAppsDirect(ctx)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]interface{}{
					"installs": installs,
					"total":    len(installs),
				})
			}

			if len(installs) == 0 {
				fmt.Println("No apps installed")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAPP\tDOMAIN_ID\tSUBDIR\tSTATUS\tCREATED")
			for _, inst := range installs {
				subdir := inst.Subdirectory
				if subdir == "" {
					subdir = "/"
				}
				appType := inst.AppType
				if appType == "" {
					appType = "wordpress"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					inst.ID, appType, inst.DomainID, subdir, inst.Status,
					inst.CreatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}
}

// ---- get ----

func newAppGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <install-id|domain-name>",
		Short: "Show one installed app (direct DB — M20-safe)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			install, err := resolveAppSpec(ctx, args[0])
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(install)
			}

			subdir := install.Subdirectory
			if subdir == "" {
				subdir = "/"
			}
			appType := install.AppType
			if appType == "" {
				appType = "wordpress"
			}
			fmt.Printf("ID:        %s\n", install.ID)
			fmt.Printf("App:       %s\n", appType)
			fmt.Printf("Domain:    %s\n", install.DomainID)
			fmt.Printf("Subdir:    %s\n", subdir)
			fmt.Printf("Admin:     %s <%s>\n", install.AdminUsername, install.AdminEmail)
			fmt.Printf("Status:    %s\n", install.Status)
			if install.LastError != "" {
				fmt.Printf("LastError: %s\n", install.LastError)
			}
			fmt.Printf("Created:   %s\n", install.CreatedAt.Format(time.RFC3339))
			return nil
		},
	}
}

// ---- install ----

// newAppInstallCmd posts an install through the shared
// api.InstallApplication service (same code path as the HTTP handler).
// Owner is auto-resolved from the domain; --user is exposed for
// admin overrides only.
//
// --param k=v repeats; values are JSON-decoded so booleans/numbers/
// objects round-trip without manual escaping. --wait blocks until the
// install row reaches a terminal status.
func newAppInstallCmd() *cobra.Command {
	var (
		appType    string
		domainSpec string
		userID     string
		subdir     string
		useWWW     bool
		params     []string
		wait       bool
		waitSec    int
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install an app on a domain (direct service — M20-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := parseParamFlags(params)
			if err != nil {
				return fmt.Errorf("--param: %w", err)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			// --domain accepts a bare name ("example.com") or a ULID;
			// names read better at the terminal and ULIDs remain valid
			// so existing scripts that pipe `jabali domain list --json`
			// keep working. resolveDomainSpec is the same helper the
			// mailbox and domain-email commands use.
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), domainSpec)
			if err != nil {
				return err
			}

			// Accept email / username / ULID for --user. Empty stays
			// empty — the handler then defaults to the domain owner.
			resolvedUserID := ""
			if userID != "" {
				owner, err := resolveUser(ctx, userID)
				if err != nil {
					return err
				}
				resolvedUserID = owner.ID
			}

			res, err := installAppDirect(ctx, api.InstallParams{
				AppType:      appType,
				UserID:       resolvedUserID,
				DomainID:     dom.ID,
				Subdirectory: subdir,
				UseWWW:       useWWW,
				Params:       parsed,
			})
			if err != nil {
				return err
			}

			if jsonOutput && !wait {
				return printJSON(res)
			}

			fmt.Printf("Install queued: %s (app=%s, status=%s)\n",
				res.Install.ID, res.Install.AppType, res.Install.Status)
			if res.Install.AdminUsername != "" {
				fmt.Printf("  admin: %s\n", res.Install.AdminUsername)
			}
			if res.AdminPassword != "" {
				fmt.Printf("  password (shown once): %s\n", res.AdminPassword)
			}

			if !wait {
				return nil
			}

			final, perr := pollInstallStatus(cmd.Context(), res.Install.ID, time.Duration(waitSec)*time.Second)
			if perr != nil {
				return perr
			}
			if jsonOutput {
				return printJSON(final)
			}
			fmt.Printf("\nFinal status: %s\n", final.Status)
			if final.LastError != "" {
				fmt.Printf("Error: %s\n", final.LastError)
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&appType, "app-type", "", "App descriptor name (see `jabali app registry`)")
	cmd.Flags().StringVar(&domainSpec, "domain", "", "Target domain name or ULID (e.g. example.com or 01KPR…)")
	cmd.Flags().StringVar(&userID, "user", "", "Owner user (email, username, or ULID; default: domain owner)")
	cmd.Flags().StringVar(&subdir, "directory", "", "Subdirectory under docroot (empty = site root)")
	cmd.Flags().StringVar(&subdir, "subdir", "", "Alias for --directory (deprecated)")
	_ = cmd.Flags().MarkHidden("subdir")
	cmd.Flags().BoolVar(&useWWW, "use-www", false, "Reachable at www.<domain> too")
	cmd.Flags().StringArrayVar(&params, "param", nil, "Per-app param: --param key=value (value is JSON; repeat for multiple)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Poll until status is ready or failed")
	cmd.Flags().IntVar(&waitSec, "wait-timeout", 600, "Seconds to wait when --wait is set")
	_ = cmd.MarkFlagRequired("app-type")
	_ = cmd.MarkFlagRequired("domain")
	return cmd
}

// parseParamFlags converts ["site_title=My Site", "is_public=true"]
// into a typed map. Values are JSON-decoded first so booleans/numbers
// round-trip; on JSON failure we try ParseBool, then fall back to the
// raw string. Quoting `"hello"` lets callers force-string a numeric.
func parseParamFlags(flags []string) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(flags))
	for _, raw := range flags {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("expected key=value, got %q", raw)
		}
		key := raw[:eq]
		val := raw[eq+1:]

		var parsed interface{}
		if err := json.Unmarshal([]byte(val), &parsed); err == nil {
			out[key] = parsed
			continue
		}
		if b, err := strconv.ParseBool(val); err == nil {
			out[key] = b
			continue
		}
		out[key] = val
	}
	return out, nil
}

// ---- delete ----

func newAppDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <install-id|domain-name>",
		Short: "Delete an installed app (direct DB + agent teardown — M20-safe)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Wider timeout than read commands because the agent has to
			// rm the docroot, drop the database, and restore the nginx
			// placeholder — all serial. Matches the HTTP path's 5-min
			// internal context.
			ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Minute)
			defer cancel()

			preview, err := resolveAppSpec(ctx, args[0])
			if err != nil {
				return err
			}
			id := preview.ID

			if !force {
				appType := preview.AppType
				if appType == "" {
					appType = "wordpress"
				}
				fmt.Printf("Delete %s install %s on domain %s? (type 'yes' to confirm): ",
					appType, preview.ID, preview.DomainID)
				var confirm string
				fmt.Scanln(&confirm)
				if !strings.EqualFold(confirm, "yes") {
					fmt.Println("Cancelled")
					return nil
				}
			}

			install, err := deleteAppDirect(ctx, id)
			if err != nil {
				return err
			}
			fmt.Printf("Application %s deleted (app=%s, domain=%s)\n",
				install.ID, install.AppType, install.DomainID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}
