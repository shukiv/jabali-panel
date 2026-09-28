// Admin service action CLI (Gitea #557). Mirrors POST /admin/services/:name/
// :action (admin_services.go): the same action allowlist (restart/start/stop/
// reload/enable/disable -> service.<action>), the same service-name regex, and
// the same self-destruct protection (api.IsPanelSelfDestruct: stop/disable on
// jabali-panel, jabali-agent, jabali-kratos, mariadb, nginx and redis-server is
// hard-blocked). Disruptive actions require --force. After a
// successful action the refreshed service status is shown. Mutations CLI-audited.
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

var (
	cliServiceActions = map[string]string{
		"restart": "service.restart", "start": "service.start", "stop": "service.stop",
		"reload": "service.reload", "enable": "service.enable", "disable": "service.disable",
	}
	cliServiceNameRe     = regexp.MustCompile(`^[a-zA-Z0-9._@-]+$`)
	cliDisruptiveActions = map[string]bool{"stop": true, "restart": true, "disable": true}
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Admin service control (start/stop/restart/reload/enable/disable)",
	}
	cmd.AddCommand(newServiceStatusCmd(), newServiceActionCmd())
	return cmd
}

func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show service status details",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return csCall(ctx, "system.service_details", nil)
		},
	}
}

func newServiceActionCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "action <name> <restart|start|stop|reload|enable|disable>",
		Short:   "Run a service action on an allowlisted unit",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, action := args[0], args[1]
			if !cliServiceNameRe.MatchString(name) {
				return fmt.Errorf("invalid service name %q", name)
			}
			verb, ok := cliServiceActions[action]
			if !ok {
				return fmt.Errorf("unsupported action %q (restart|start|stop|reload|enable|disable)", action)
			}
			if api.IsPanelSelfDestruct(name, action) {
				return fmt.Errorf("%s on %s would brick the management plane and is blocked", action, name)
			}
			if cliDisruptiveActions[action] && !force {
				return fmt.Errorf("%s on %s is disruptive; re-run with --force", action, name)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, verb, map[string]any{"name": name})
			if err != nil {
				cliAuditErr(ctx, "service."+action, "service", name, nil)
				return fmt.Errorf("agent %s: %w", verb, err)
			}
			cliAuditOK(ctx, "service."+action, "service", name, nil)
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			// Show refreshed status (best-effort).
			if statusRaw, serr := sharedAgent.Call(ctx, "system.service_details", nil); serr == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "--- refreshed status ---")
				os.Stdout.Write(statusRaw)
				os.Stdout.Write([]byte{'\n'})
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm a disruptive action (stop/restart/disable)")
	return cmd
}
