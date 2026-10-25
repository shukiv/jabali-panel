// PHP-defense / Snuffleupagus CLI (Gitea #552). Mirrors the Security
// Snuffleupagus API (security_snuffleupagus.go): status, mode, rule overrides,
// and incidents. Mode/rule changes write the same DB rows the handlers do and
// then run the SnuffleupagusReconciler (renders active.rules + agent
// snuffleupagus.apply_rules) — identical apply path as the GUI.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newPhpDefenseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "php-defense",
		Aliases: []string{"snuffleupagus"},
		Short:   "PHP-defense (Snuffleupagus) status / mode / rules / incidents",
	}
	cmd.AddCommand(
		csPassthroughCmd("status", "Show Snuffleupagus status", "snuffleupagus.status"),
		newPhpDefenseModeCmd(),
		newPhpDefenseRulesCmd(),
		newPhpDefenseRuleToggleCmd(),
		newPhpDefenseIncidentsCmd(),
		newPhpDefenseIncidentsClearCmd(),
	)
	return cmd
}

func snuffReconcile(ctx context.Context) error {
	r := &reconciler.SnuffleupagusReconciler{
		Repo:  repository.NewSnuffleupagusRepository(sharedDB),
		Agent: sharedAgent,
	}
	return r.Reconcile(ctx)
}

func newPhpDefenseModeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "mode <off|simulation|enforce>",
		Short:   "Set the Snuffleupagus mode (renders + applies active.rules)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := models.SnuffleupagusMode(args[0])
			switch mode {
			case models.SnuffleupagusModeOff, models.SnuffleupagusModeSimulation, models.SnuffleupagusModeEnforce:
			default:
				return fmt.Errorf("mode must be off|simulation|enforce")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			if err := repository.NewSnuffleupagusRepository(sharedDB).UpdateState(ctx, mode, nil, nil); err != nil {
				return fmt.Errorf("persist mode: %w", err)
			}
			if err := snuffReconcile(ctx); err != nil {
				return fmt.Errorf("apply rules: %w", err)
			}
			cliAuditOK(ctx, "php_defense.mode_set", "snuffleupagus", string(mode), nil)
			fmt.Fprintf(cmd.OutOrStdout(), "Snuffleupagus mode=%s applied\n", mode)
			return nil
		},
	}
}

func newPhpDefenseRulesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rules",
		Short:   "List rule overrides + current mode (bundle catalog is GUI-only)",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			repo := repository.NewSnuffleupagusRepository(sharedDB)
			st, _ := repo.GetState(ctx)
			ovs, err := repo.ListOverrides(ctx)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{"mode": st.Mode, "overrides": ovs})
			}
			if st != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "mode=%s\n\n", st.Mode)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "RULE\tENABLED\tREASON")
			for _, o := range ovs {
				reason := ""
				if o.Reason != nil {
					reason = *o.Reason
				}
				fmt.Fprintf(tw, "%s\t%v\t%s\n", o.RuleName, o.Enabled, reason)
			}
			return tw.Flush()
		},
	}
}

func newPhpDefenseRuleToggleCmd() *cobra.Command {
	var enable, disable bool
	var reason string
	cmd := &cobra.Command{
		Use:     "rule-toggle <rule-name>",
		Short:   "Enable or disable a Snuffleupagus rule (renders + applies)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable == disable {
				return fmt.Errorf("pass exactly one of --enable or --disable")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			ov := &models.SnuffleupagusRuleOverride{RuleName: args[0], Enabled: enable}
			if reason != "" {
				ov.Reason = &reason
			}
			if err := repository.NewSnuffleupagusRepository(sharedDB).UpsertOverride(ctx, ov); err != nil {
				return fmt.Errorf("persist override: %w", err)
			}
			if err := snuffReconcile(ctx); err != nil {
				return fmt.Errorf("apply rules: %w", err)
			}
			cliAuditOK(ctx, "php_defense.rule_toggle", "snuffleupagus_rule", args[0], nil)
			fmt.Fprintf(cmd.OutOrStdout(), "rule %s enabled=%v applied\n", args[0], enable)
			return nil
		},
	}
	cmd.Flags().BoolVar(&enable, "enable", false, "enable the rule")
	cmd.Flags().BoolVar(&disable, "disable", false, "disable the rule")
	cmd.Flags().StringVar(&reason, "reason", "", "optional reason for the override")
	return cmd
}

func newPhpDefenseIncidentsCmd() *cobra.Command {
	var rule string
	var limit int
	cmd := &cobra.Command{
		Use:     "incidents",
		Short:   "List Snuffleupagus incidents",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			opts := repository.IncidentListOptions{Rule: rule}
			if limit > 0 {
				opts.Limit = limit
			}
			rows, _, err := repository.NewSnuffleupagusRepository(sharedDB).ListIncidents(ctx, opts)
			if err != nil {
				return err
			}
			return printJSON(rows)
		},
	}
	cmd.Flags().StringVar(&rule, "rule", "", "filter by rule name")
	cmd.Flags().IntVar(&limit, "limit", 50, "max rows")
	return cmd
}

// newPhpDefenseIncidentsClearCmd mirrors Clear history on the admin page
// (DELETE /admin/security/snuffleupagus/incidents, GH #2262).
func newPhpDefenseIncidentsClearCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "incidents-clear",
		Short:   "Delete every recorded Snuffleupagus incident",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return fmt.Errorf("this deletes the whole incident history; pass --yes to confirm")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			n, err := repository.NewSnuffleupagusRepository(sharedDB).DeleteAllIncidents(ctx)
			if err != nil {
				return fmt.Errorf("clear incidents: %w", err)
			}
			cliAuditOK(ctx, "php_defense.incidents_clear", "snuffleupagus_incidents", "all", nil)
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %d incidents\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deleting the whole incident history")
	return cmd
}
