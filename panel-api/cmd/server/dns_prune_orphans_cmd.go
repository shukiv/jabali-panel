package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// dns_prune_orphans_cmd.go — GH #1620 remediation half.
//
// #1629 made DeleteZone tear the child rows (records, domainmetadata, comments,
// cryptokeys) down before the parent `domains` row, so a zone delete no longer
// strands children. That is forward-only: a box that deleted a domain BEFORE
// #1629 still carries the orphaned child rows, and they can keep being served
// (from the backend or its caches) and collide as "duplicate records" when the
// domain is re-added.
//
// This is the one-time cleanup #1629 does not do. NOTE this is distinct from
// `jabali domain prune-orphans` (nginx sites-enabled vs DB) and from
// `jabali dns prune-service-records` (control-plane dns_records): those act on
// panel state, this acts on the agent-side PowerDNS backend tables. The pdns DB
// is agent-side, so the work runs in the agent (dns.reap-orphans), scoped
// strictly to rows whose parent `domains` row is gone, with a cache purge after.

// dnsReapOrphansResult mirrors the agent's dns.reap-orphans response.
type dnsReapOrphansResult struct {
	Applied bool           `json:"applied"`
	Counts  map[string]int `json:"counts"`
	Deleted map[string]int `json:"deleted"`
	Names   []string       `json:"names"`
}

func newDNSPruneOrphanRecordsCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "prune-orphan-records",
		Short: "Remove PowerDNS backend rows left behind by a pre-#1629 zone delete (GH #1620)",
		Long: "Removes rows in the PowerDNS backend (records, domainmetadata, comments, cryptokeys)\n" +
			"whose parent `domains` row no longer exists — orphans left behind when a domain was\n" +
			"deleted BEFORE the #1629 teardown fix. They keep answering off the pdns caches and\n" +
			"collide as duplicate records when the domain is re-added.\n\n" +
			"This is NOT the same as `domain prune-orphans` (nginx vhosts) or `dns\n" +
			"prune-service-records` (control-plane dns_records) — it operates on the agent-side\n" +
			"PowerDNS tables.\n\n" +
			"The sweep is scoped strictly to `domain_id NOT IN (SELECT id FROM domains)`; a\n" +
			"re-added domain's records (which carry a new domain_id) are never touched. The agent\n" +
			"refuses to run if the domains table is empty (that would make every record look\n" +
			"orphaned). After deleting, it purges the pdns Auth + recursor caches for the affected\n" +
			"names so they stop resolving immediately.\n\n" +
			"Dry-run by default. Re-run with --apply to delete.",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			raw, err := sharedAgent.Call(ctx, "dns.reap-orphans", map[string]any{"apply": apply})
			if err != nil {
				return fmt.Errorf("reap orphans: %w", err)
			}
			var resp dnsReapOrphansResult
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fmt.Errorf("decode agent response: %w", err)
			}

			if jsonOutput {
				return printJSON(resp)
			}

			total := 0
			for _, n := range resp.Counts {
				total += n
			}
			if total == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No orphaned PowerDNS rows found — nothing to prune.")
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TABLE\tORPHAN ROWS")
			for _, t := range sortedCountKeys(resp.Counts) {
				fmt.Fprintf(tw, "%s\t%d\n", t, resp.Counts[t])
			}
			_ = tw.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d orphan row(s) across %d distinct record name(s).\n",
				total, len(resp.Names))

			if !resp.Applied {
				fmt.Fprintln(cmd.OutOrStdout(),
					"\nThis is a DRY RUN — nothing changed. Re-run with --apply to delete these rows\n"+
						"and purge the pdns caches for the affected names.")
				return nil
			}

			deleted := 0
			for _, n := range resp.Deleted {
				deleted += n
			}
			cliAuditOK(ctx, "dns.reap_orphans", "pdns_records", fmt.Sprintf("%d", deleted), nil)
			fmt.Fprintf(cmd.OutOrStdout(),
				"\nremoved %d orphan row(s) and purged the pdns Auth + recursor caches for the\n"+
					"%d affected name(s).\n", deleted, len(resp.Names))
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually delete (default is a dry run)")
	return cmd
}

// sortedCountKeys returns the keys of a map[string]int in ascending order, for
// stable report output.
func sortedCountKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
