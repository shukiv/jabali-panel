package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// appsec_exclusion_cmd.go — JAB-227.
//
// Operator-managed CRS false-positive exclusions. The built-in set in
// internal/appseccfg covers what jabali ships knowing about; this covers what
// only the operator can see — a customer's webhook whose message bodies read as
// RFI, a plugin route that trips libinjection on ordinary content.
//
// Before this, the options were hand-editing a file on the box (and knowing that
// only ctl:ruleRemoveById has any effect, since the target-scoped forms are
// silent no-ops) or leaving the customer blocked.

func crsExclRepo() repository.CRSRuleExclusionRepository {
	return repository.NewCRSRuleExclusionRepository(sharedDB)
}

func newAppSecExclusionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exclusion",
		Short: "Manage operator CRS false-positive exclusions",
		Long: "Exclude a specific CRS rule for a specific host + path.\n\n" +
			"Scope is mandatory. The only removal construct that works in CrowdSec's engine\n" +
			"drops the rule for the whole matched request, so every exclusion must name both\n" +
			"a host and a URI prefix — there is no whole-host or whole-server form.\n\n" +
			"Find the rule to exclude with: cscli alerts inspect <id>  (look at rule_name).",
	}
	cmd.AddCommand(newAppSecExclusionAddCmd(), newAppSecExclusionListCmd(), newAppSecExclusionRmCmd())
	return cmd
}

func newAppSecExclusionAddCmd() *cobra.Command {
	var host, uriPrefix, ruleID, note string
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add an exclusion (host + path + rule are all required)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			e := appseccfg.Exclusion{Host: host, URIPrefix: uriPrefix, RuleID: ruleID, Note: note}
			if err := appseccfg.ValidateExclusion(e); err != nil {
				return err
			}
			row := &models.CRSRuleExclusion{
				ID: ids.NewULID(), Host: host, URIPrefix: uriPrefix, RuleID: ruleID, Note: note,
			}
			if err := crsExclRepo().Create(ctx, row); err != nil {
				return fmt.Errorf("save exclusion: %w", err)
			}
			cliAuditOK(ctx, "appsec.exclusion_add", "crs_rule_exclusion", row.ID, nil)
			if jsonOutput {
				return printJSON(map[string]any{"id": row.ID, "host": host, "uri_prefix": uriPrefix, "rule_id": ruleID})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"added %s — rule %s excluded for %s%s\n"+
					"  apply with: jabali appsec render-config --reconcile\n",
				row.ID, ruleID, host, uriPrefix)
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "hostname the exclusion applies to (required)")
	cmd.Flags().StringVar(&uriPrefix, "uri-prefix", "", "URI prefix the exclusion applies to, e.g. /wp-json/x/ (required)")
	cmd.Flags().StringVar(&ruleID, "rule", "", "CRS rule ID to exclude, e.g. 942100 (required)")
	cmd.Flags().StringVar(&note, "note", "", "why this exclusion exists")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("uri-prefix")
	_ = cmd.MarkFlagRequired("rule")
	return cmd
}

func newAppSecExclusionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List operator CRS exclusions",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			rows, err := crsExclRepo().List(ctx)
			if err != nil {
				return fmt.Errorf("list exclusions: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"exclusions": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No operator CRS exclusions.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tHOST\tURI PREFIX\tRULE\tNOTE")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Host, r.URIPrefix, r.RuleID, r.Note)
			}
			return w.Flush()
		},
	}
}

func newAppSecExclusionRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <id>",
		Short:   "Remove an exclusion by id",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if err := crsExclRepo().DeleteByID(ctx, args[0]); err != nil {
				return fmt.Errorf("delete exclusion: %w", err)
			}
			cliAuditOK(ctx, "appsec.exclusion_rm", "crs_rule_exclusion", args[0], nil)
			if jsonOutput {
				return printJSON(map[string]any{"id": args[0], "deleted": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"removed %s\n  apply with: jabali appsec render-config --reconcile\n", args[0])
			return nil
		},
	}
}
