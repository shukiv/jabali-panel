package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// appsec_flarum_sync_cmd.go — GH #1650.
//
// A Flarum install registers its own scoped CRS 920450 exclusion. Forums
// installed before that shipped have none, and nothing adds one automatically:
// doing so would relax 920450 on every existing forum at the next update
// without an operator decision. This command is that decision, taken once.

// flarumSyncReport is what `appsec flarum-sync` did.
type flarumSyncReport struct {
	Installs int                           `json:"installs"`
	Added    []appseccfg.Exclusion         `json:"added"`
	Result   appseccfg.OperatorApplyResult `json:"result"`
}

// runFlarumSync registers the exclusion for every ready Flarum install and
// applies the result once. With no Flarum installs it changes nothing.
func runFlarumSync(ctx context.Context, listIDs func(context.Context, string) ([]string, error), d appsecops.Deps) (flarumSyncReport, error) {
	ids, err := listIDs(ctx, appsecops.AppTypeFlarum)
	if err != nil {
		return flarumSyncReport{}, fmt.Errorf("list Flarum installs: %w", err)
	}
	rep := flarumSyncReport{Installs: len(ids), Added: []appseccfg.Exclusion{}}
	if len(ids) == 0 {
		return rep, nil
	}
	added, res, err := appsecops.SyncFlarumInstalls(ctx, d, ids)
	if added != nil {
		rep.Added = added
	}
	rep.Result = res
	return rep, err
}

func printFlarumSyncReport(out io.Writer, rep flarumSyncReport) {
	if rep.Installs == 0 {
		fmt.Fprintln(out, "No ready Flarum installs — nothing to do.")
		return
	}
	for _, e := range rep.Added {
		fmt.Fprintf(out, "added   rule %s excluded for %s%s\n", e.RuleID, e.Host, e.URIPrefix)
	}
	fmt.Fprintf(out, "%d Flarum install(s), %d exclusion(s) added (the rest were already covered).\n",
		rep.Installs, len(rep.Added))
	switch {
	case rep.Result.Skipped != "":
		fmt.Fprintf(out, "not applied: %s\n", rep.Result.Skipped)
	case rep.Result.Reloaded:
		fmt.Fprintln(out, "applied: operator before-plugin updated, crowdsec reloaded")
	default:
		fmt.Fprintln(out, "applied: operator before-plugin already current")
	}
}

func newAppSecFlarumSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "flarum-sync",
		Short: "Add the scoped CRS 920450 exclusion for every existing Flarum forum",
		Long: "Flarum sends PATCH and DELETE as POST + X-HTTP-Method-Override, which CRS\n" +
			"rule 920450 blocks. New Flarum installs register a scoped exclusion (rule\n" +
			"920450, the forum's host, its /api/ path) automatically. This adds the same\n" +
			"exclusion for every ready Flarum install that has none yet, and applies the\n" +
			"result with one crowdsec reload. Safe to re-run.",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			installs := repository.NewApplicationInstallRepository(sharedDB)
			rep, err := runFlarumSync(ctx, installs.ListReadyIDsByAppType, appsecops.Deps{
				Agent:      sharedAgent,
				Exclusions: crsExclRepo(),
				HostModes:  repository.NewCRSHostModeRepository(sharedDB),
				Installs:   installs,
				Domains:    repository.NewDomainRepository(sharedDB),
			})
			if len(rep.Added) > 0 {
				cliAuditOK(ctx, "appsec.flarum_sync", "crs_rule_exclusion", fmt.Sprintf("%d added", len(rep.Added)), nil)
			}
			if err != nil {
				printFlarumSyncReport(cmd.ErrOrStderr(), rep)
				return err
			}
			if jsonOutput {
				return printJSON(rep)
			}
			printFlarumSyncReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
}
