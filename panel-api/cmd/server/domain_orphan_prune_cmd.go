package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// jabali domain prune-orphans:
//   - Asks the agent for the live nginx sites-enabled list (domain.list).
//   - Fetches every domain row from the panel DB.
//   - Diffs the two: any site present on the agent but with no matching
//     domain row (or "<domain>-mail" suffix that isn't backed by a
//     domain row with email_enabled=1) is an orphan.
//   - Lists orphans + (optionally) calls domain.delete on the agent for
//     each one. Operator runs --dry-run by default; --apply removes.
//
// Why a CLI tool, not auto-cleanup in ReconcileAll: orphan deletion is
// destructive (drops the nginx vhost + DKIM keypair + Stalwart Domain
// row when the orphan name was an email vhost). The reconciler stays
// log-only on orphans for safety; an operator with context decides
// when to actually delete.

func newDomainPruneOrphansCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "prune-orphans",
		Short: "List sites in nginx sites-enabled that have no panel DB row (and optionally delete them)",
		Long: `Compares the agent's live nginx sites-enabled list against the
panel DB. Any site name that doesn't match an existing domain row (and
isn't a system site like 000-default) is reported as an orphan.

Default mode is dry-run — orphans are listed but nothing is deleted.
Pass --apply to issue domain.delete on the agent for each orphan. The
delete call tears down the nginx vhost, removes DKIM keys, and clears
the Stalwart mail Domain row when applicable. It is irreversible.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			// 1. Live agent state.
			raw, err := sharedAgent.Call(ctx, "domain.list", map[string]any{})
			if err != nil {
				return fmt.Errorf("agent domain.list: %w", err)
			}
			var resp struct {
				Sites []string `json:"sites"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fmt.Errorf("parse agent response: %w", err)
			}

			// 2. DB state — every domain name (enabled or disabled).
			domains, err := listDomainsDirect(ctx)
			if err != nil {
				return fmt.Errorf("list domains: %w", err)
			}
			knownDomain := make(map[string]bool, len(domains))
			for _, d := range domains {
				knownDomain[d.Name] = true
			}

			// 3. Diff via the pure-logic helper (testable in isolation).
			orphans := computeOrphans(resp.Sites, knownDomain)

			if jsonOutput {
				return printJSON(map[string]any{
					"orphans":     orphans,
					"agent_sites": resp.Sites,
					"db_domains":  len(domains),
					"applied":     apply,
				})
			}

			if len(orphans) == 0 {
				fmt.Println("No orphan sites found.")
				return nil
			}

			fmt.Printf("Found %d orphan site(s) on agent (no DB row):\n", len(orphans))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SITE\tACTION")
			for _, site := range orphans {
				action := "(dry-run — would delete)"
				if apply {
					action = "deleting…"
				}
				fmt.Fprintf(w, "%s\t%s\n", site, action)
			}
			_ = w.Flush()

			if !apply {
				fmt.Println()
				fmt.Println("Re-run with --apply to actually delete these.")
				return nil
			}

			// 4. Apply: agent.domain.delete each orphan. Strip -mail since
			// the agent's domain.delete handler operates on the canonical
			// domain name and tears down both vhosts.
			fmt.Println()
			fail := 0
			for _, site := range orphans {
				name := orphanDomainName(site)
				delCtx, delCancel := context.WithTimeout(ctx, 30*time.Second)
				_, err := sharedAgent.Call(delCtx, "domain.delete", map[string]string{"domain": name})
				delCancel()
				if err != nil {
					fmt.Printf("  %s: FAILED — %v\n", site, err)
					fail++
					continue
				}
				fmt.Printf("  %s: deleted\n", site)
			}
			if fail > 0 {
				return fmt.Errorf("%d orphan deletion(s) failed", fail)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually delete orphans (default: dry-run)")
	return cmd
}

// computeOrphans returns nginx site names that have no matching domain
// row in the panel DB. System sites (default, jabali-panel, etc.) are
// filtered out. M6 webmail vhosts named "<domain>-mail" are not
// orphans when the underlying domain is known — the suffix is stripped
// before the lookup.
//
// Pure function, no I/O — tested via TestComputeOrphans.
// orphanDomainName is the domain an agent site belongs to: a domain's derived
// vhosts (`<domain>-mail`, `<domain>-mta-sts`) belong to the domain.
func orphanDomainName(site string) string {
	for _, suffix := range []string{"-mail", "-mta-sts"} {
		if name, ok := strings.CutSuffix(site, suffix); ok {
			return name
		}
	}
	return site
}

func computeOrphans(agentSites []string, knownDomain map[string]bool) []string {
	systemSites := map[string]bool{
		"default":          true,
		"default-ssl":      true,
		"000-default":      true,
		"000-default-ssl":  true,
		"jabali-panel":     true, // panel itself
		"jabali-panel-ssl": true,
		"jabali-pma":       true, // phpMyAdmin
		"jabali-adminer":   true,
		"jabali-webmail":   true,
		// The *.preview.<hostname> catch-all vhost; the reconciler's
		// knownSystemSites lists it too.
		"jabali-preview-fallback": true,
	}
	out := make([]string, 0, len(agentSites))
	for _, site := range agentSites {
		if systemSites[site] {
			continue
		}
		if knownDomain[orphanDomainName(site)] {
			continue
		}
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}
