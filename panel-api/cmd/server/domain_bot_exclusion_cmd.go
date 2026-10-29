// `jabali domain bot-exclusion` — paths of a domain that skip the AppSec
// bot-detection challenge (GH #2328). The same rows and checks as the admin
// API (api.BuildBotChallengeExclusion); the panel reconciler applies a change
// on its next pass, within a minute.
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

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newDomainBotExclusionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bot-exclusion",
		Short: "Manage the paths of a domain that skip the bot-detection challenge",
	}
	cmd.AddCommand(newDomainBotExclusionListCmd(), newDomainBotExclusionAddCmd(), newDomainBotExclusionDeleteCmd())
	return cmd
}

func newDomainBotExclusionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <domain>",
		Short:   "List the paths of a domain that skip the bot-detection challenge",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			rows, err := repository.NewBotChallengeExclusionRepository(sharedDB).ListByDomain(ctx, d.ID)
			if err != nil {
				return err
			}
			views := make([]api.BotChallengeExclusionView, 0, len(rows))
			for _, r := range rows {
				views = append(views, api.NewBotChallengeExclusionView(r))
			}
			if jsonOutput {
				return printJSON(views)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tPATH\tMETHODS\tNOTE")
			for _, v := range views {
				methods := strings.Join(v.Methods, ",")
				if methods == "" {
					methods = "any"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", v.ID, v.PathPrefix, methods, v.Note)
			}
			return tw.Flush()
		},
	}
}

func newDomainBotExclusionAddCmd() *cobra.Command {
	var methods []string
	var note string
	cmd := &cobra.Command{
		Use:   "add <domain> <path-prefix>",
		Short: "Let requests to a path of a domain skip the bot-detection challenge",
		Long: "Requests whose path starts with <path-prefix> skip the bot-detection challenge on the domain\n" +
			"and www.<domain>, for the given methods (all methods when none are given). The path is matched\n" +
			"decoded and without the query string. The WAF, rate limits and IP bans still apply.",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			row, err := api.BuildBotChallengeExclusion(d.ID, args[1], methods, note)
			if err != nil {
				return err
			}
			repo := repository.NewBotChallengeExclusionRepository(sharedDB)
			n, err := repo.CountByDomain(ctx, d.ID)
			if err != nil {
				return err
			}
			if n >= appseccfg.MaxBotPathExclusionsPerDomain {
				return fmt.Errorf("%s already lists %d paths, the most a domain can", d.Name, n)
			}
			if err := repo.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					return fmt.Errorf("%s already lists %s for these methods", d.Name, row.PathPrefix)
				}
				return fmt.Errorf("add path: %w", err)
			}
			cliAuditOK(ctx, "domain.bot_exclusion_add", "bot_challenge_exclusion", row.ID, &d.UserID)
			if jsonOutput {
				return printJSON(api.NewBotChallengeExclusionView(*row))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s on %s (id=%s). Applied in about a minute while bot detection is on.\n", row.PathPrefix, d.Name, row.ID)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&methods, "method", nil, "only these methods ("+strings.Join(appseccfg.BotChallengeMethods, ", ")+"); repeat or comma-separate")
	cmd.Flags().StringVar(&note, "note", "", "why the path skips the challenge")
	return cmd
}

func newDomainBotExclusionDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <domain> <id>",
		Short:   "Remove a path of a domain from the bot-detection exclusions",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			if err := repository.NewBotChallengeExclusionRepository(sharedDB).DeleteForDomain(ctx, d.ID, args[1]); err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("%s has no bot-detection exclusion %s", d.Name, args[1])
				}
				return fmt.Errorf("delete path: %w", err)
			}
			cliAuditOK(ctx, "domain.bot_exclusion_delete", "bot_challenge_exclusion", args[1], &d.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s from %s.\n", args[1], d.Name)
			return nil
		},
	}
}
