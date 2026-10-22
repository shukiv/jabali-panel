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
			// The flags parsed; an error from here on is about the sites, and
			// the usage text would only bury it (or, with --json, trail the
			// JSON the Repair Center reads).
			cmd.SilenceUsage = true
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			// 1. Live agent state.
			sites, err := listAgentSites(ctx, sharedAgent.Call)
			if err != nil {
				return err
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
			orphans := computeOrphans(sites, knownDomain)

			// 4. Apply, before any output, so the --json result the Repair
			// Center reads says what was deleted (GH #2237).
			deleted, failed := []string{}, []orphanPruneFailure{}
			if apply && len(orphans) > 0 {
				deleted, failed = pruneOrphanSites(cmd.Context(), sharedAgent.Call, orphans)
			}

			if jsonOutput {
				if err := printJSON(map[string]any{
					"orphans":     orphans,
					"agent_sites": sites,
					"db_domains":  len(domains),
					"applied":     apply,
					"deleted":     deleted,
					"failed":      failed,
				}); err != nil {
					return err
				}
				return orphanPruneErr(failed)
			}

			if len(orphans) == 0 {
				fmt.Println("No orphan sites found.")
				return nil
			}

			fmt.Printf("Found %d orphan site(s) on agent (no DB row):\n", len(orphans))
			failedWhy := make(map[string]string, len(failed))
			for _, f := range failed {
				failedWhy[f.Site] = f.Error
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SITE\tACTION")
			for _, site := range orphans {
				action := "(dry-run — would delete)"
				if apply {
					action = "deleted"
					if why, ok := failedWhy[site]; ok {
						action = "FAILED — " + why
					}
				}
				fmt.Fprintf(w, "%s\t%s\n", site, action)
			}
			_ = w.Flush()

			if !apply {
				fmt.Println()
				fmt.Println("Re-run with --apply to actually delete these.")
				return nil
			}
			return orphanPruneErr(failed)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually delete orphans (default: dry-run)")
	return cmd
}

// orphanPruneFailure is an orphan site that is still there after the prune,
// with the reason.
type orphanPruneFailure struct {
	Site  string `json:"site"`
	Error string `json:"error"`
}

// listAgentSites returns the agent's nginx sites-enabled names (domain.list).
func listAgentSites(ctx context.Context, call agentCaller) ([]string, error) {
	raw, err := call(ctx, "domain.list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("agent domain.list: %w", err)
	}
	var resp struct {
		Sites []string `json:"sites"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse agent response: %w", err)
	}
	return resp.Sites, nil
}

// pruneOrphanSites deletes each orphan with the agent's domain.delete, then
// lists sites-enabled again: a site counts as deleted only once it is gone
// from that list, so a delete that removed nothing is reported, not counted.
// A domain's derived vhosts (`<domain>-mail`, …) go with one domain.delete of
// the domain, which tears them all down.
func pruneOrphanSites(ctx context.Context, call agentCaller, orphans []string) (deleted []string, failed []orphanPruneFailure) {
	deleteErr := map[string]error{} // domain name → its domain.delete error
	for _, site := range orphans {
		name := orphanDomainName(site)
		if _, done := deleteErr[name]; done {
			continue
		}
		delCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := call(delCtx, "domain.delete", map[string]string{"domain": name})
		cancel()
		deleteErr[name] = err
	}

	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remaining, listErr := listAgentSites(listCtx, call)
	stillThere := make(map[string]bool, len(remaining))
	for _, site := range remaining {
		stillThere[site] = true
	}

	deleted, failed = []string{}, []orphanPruneFailure{}
	for _, site := range orphans {
		switch err := deleteErr[orphanDomainName(site)]; {
		case err != nil:
			failed = append(failed, orphanPruneFailure{Site: site, Error: err.Error()})
		case listErr != nil:
			failed = append(failed, orphanPruneFailure{Site: site, Error: "deleted, but could not check that it is gone: " + listErr.Error()})
		case stillThere[site]:
			failed = append(failed, orphanPruneFailure{Site: site, Error: "still in /etc/nginx/sites-enabled after the delete"})
		default:
			deleted = append(deleted, site)
		}
	}
	return deleted, failed
}

// orphanPruneErr makes the command exit non-zero when a site was not deleted.
func orphanPruneErr(failed []orphanPruneFailure) error {
	if len(failed) > 0 {
		return fmt.Errorf("%d orphan deletion(s) failed", len(failed))
	}
	return nil
}

// computeOrphans returns nginx site names that have no matching domain
// row in the panel DB. System sites (default, jabali-panel, etc.) are
// filtered out. M6 webmail vhosts named "<domain>-mail" are not
// orphans when the underlying domain is known — the suffix is stripped
// before the lookup.
//
// Pure function, no I/O — tested via TestComputeOrphans.
// orphanDomainName is the domain an agent site belongs to: a domain's derived
// vhosts (`<domain>-mail`, `<domain>-mta-sts`, a Docker app's
// `<domain>-dockerapp`) belong to the domain.
func orphanDomainName(site string) string {
	for _, suffix := range []string{"-mail", "-mta-sts", "-dockerapp"} {
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
		// The Stalwart WebAdmin proxy (mail.webadmin.apply), on while the
		// admin has it on; the reconciler's knownSystemSites lists it too.
		"jabali-stalwart-admin": true,
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
