package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
)

// newAppSecExplainCmd answers the question every AppSec false-positive report
// starts with and nothing could previously answer: WHICH rule blocked this?
//
// /var/log/crowdsec.log records only the outcome —
//
//	"crowdsecurity/appsec-native by ip 1.2.3.4 (IT/8075) : 4h ban"
//
// — no rule, no URI, no matched variable. Every FP in the JAB-193 cluster
// (195/196/197/198) stalled on that, and ADR-0124 documents the cost of
// guessing instead: the first triage excluded 901340, which scores nothing.
//
// The data was there the whole time, in `cscli alerts inspect -d` meta. This
// groups it by rule + URI so the pattern behind a complaint is visible, and an
// exclusion can be written against a rule that actually fired. The grouping
// lives in appsecops so the admin UI (GH #1649) shows the same patterns.

// printInlineBlocks reports 403s that never became a ban.
//
// These are the ones that made JAB-193 hard: an inline denial creates no
// decision and no alert, so `cscli decisions list` and `cscli alerts list` are
// both empty for the affected IP and the operator concludes CrowdSec was not
// involved. crowdsec.log is the only record, and it carries less than an alert
// does — score families and source IP, no rule id and no URI.
//
// Kept in its own section, deliberately: presenting these next to the
// alert-backed rows above would imply a precision they do not have.
func printInlineBlocks(blocks []appsecops.InlineBlock) {
	if len(blocks) == 0 {
		return
	}
	fmt.Printf("\nInline 403s with no alert (%d): these never became a ban, so cscli\n", len(blocks))
	fmt.Println("shows nothing for them. crowdsec.log records no rule id and no URI.")
	for _, g := range appsecops.GroupInlineBlocks(blocks) {
		fmt.Printf("  %-42s %d block(s)   last: %s\n", g.SourceIP, g.Count, g.LastScores)
	}
	fmt.Println("\nTo get the URI for these, correlate the IP and timestamp against the")
	fmt.Println("tenant's nginx access log (403 responses). The score family above tells")
	fmt.Println("you which CRS group to look in — sql_injection, rce, php_injection, lfi.")
}

func newAppSecExplainCmd() *cobra.Command {
	var limit int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Show which CRS rules recently blocked requests (AppSec FP triage)",
		Long: `Groups recent CrowdSec AppSec inband blocks by rule and target URI.

Use this before writing a CRS exclusion. The jabali-before plugin
(ADR-0124) takes surgical ctl:ruleRemoveTargetById directives, and those
are only correct if aimed at the rule that actually scored.

Caution: the rule_name field reports only the FIRST matched id, which on a
real block is routinely 901340 — the CRS body-inspection enabler, which
contributes no score. Exclude that and nothing changes. The scoring rule is
usually another entry in the rule id list.

The same view is in the admin panel under Security → CrowdSec → WAF exclusions.`,
		PreRunE: requireAgent,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			raw, err := sharedAgent.Call(ctx, appsecops.EventsVerb, map[string]any{
				"limit": limit,
			})
			if err != nil {
				return fmt.Errorf("query appsec events: %w", err)
			}

			var resp appsecops.EventsResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fmt.Errorf("decode agent response: %w", err)
			}

			if jsonOut {
				out, _ := json.MarshalIndent(resp, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			if len(resp.Events) == 0 && len(resp.InlineBlocks) == 0 {
				fmt.Printf("No AppSec blocks found in the last %d alert(s).\n", resp.AlertsScanned)
				fmt.Println("If a user is reporting a 403, confirm AppSec is in block mode and that the")
				fmt.Println("request actually reached it — an nginx-level deny never becomes an alert.")
				return nil
			}

			patterns := appsecops.GroupEvents(resp.Events)
			fmt.Printf("AppSec blocks across the last %d alert(s) — %d event(s), %d distinct pattern(s):\n\n",
				resp.AlertsScanned, len(resp.Events), len(patterns))
			for _, p := range patterns {
				fmt.Printf("  %d block(s), %d distinct source IP(s)\n", p.Count, p.DistinctIPs)
				switch {
				case len(p.Detections) > 0:
					fmt.Printf("    scored by: %s   <- exclude one of THESE\n", strings.Join(p.Detections, ", "))
				case len(p.Other) > 0:
					fmt.Printf("    scored by: (no rule an exclusion can target — see below)\n")
				default:
					fmt.Printf("    scored by: (none identified — only infrastructure rules matched)\n")
				}
				for _, o := range p.Other {
					fmt.Printf("    not CRS  : %s (%s)\n", o.ID, o.Note)
				}
				for _, i := range p.Infra {
					fmt.Printf("    also     : %s (%s)\n", i.ID, i.Note)
				}
				fmt.Printf("    host     : %s\n", p.Host)
				fmt.Printf("    uri      : %s\n", p.URI)
				fmt.Printf("    first at : %s\n\n", p.FirstAt)
			}

			fmt.Println("Writing an exclusion:")
			fmt.Println("  - Ignore 901340 if present. It enables body inspection and scores nothing;")
			fmt.Println("    excluding it changes nothing (ADR-0124 was written after that mistake).")
			fmt.Println("  - Target the rule that scored, and scope it to the narrowest ARGS/URI that")
			fmt.Println("    reproduces — internal/appseccfg/appseccfg.go holds the jabali-before rules.")
			fmt.Println("  - Many distinct source IPs on one URI reads as a false positive.")
			fmt.Println("    One IP across many URIs usually reads as an actual attacker.")
			printInlineBlocks(resp.InlineBlocks)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 25, "how many recent AppSec alerts to inspect (max 200)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON instead of the grouped report")
	return cmd
}
