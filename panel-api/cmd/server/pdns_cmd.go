package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newPdnsCmd mounts `jabali pdns …`. Only child currently is `backfill`.
func newPdnsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pdns",
		Short: "PowerDNS helpers (recursor forwarders, etc.)",
	}
	cmd.AddCommand(newPdnsBackfillCmd())
	cmd.AddCommand(newPdnsDNSSECCmd())
	return cmd
}

func newPdnsDNSSECCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dnssec",
		Short: "Per-domain DNSSEC management (ADR-0057)",
	}
	cmd.AddCommand(
		newPdnsDNSSECEnableCmd(),
		newPdnsDNSSECDisableCmd(),
		newPdnsDNSSECDSCmd(),
		newPdnsDNSSECStatusCmd(),
	)
	return cmd
}

func newPdnsDNSSECEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "enable <domain>",
		Short:   "Enable DNSSEC for a zone (creates KSK+ZSK, rectifies, persists keys)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				return fmt.Errorf("lookup domain %q: %w", args[0], err)
			}
			raw, err := sharedAgent.Call(ctx, "dns.dnssec_enable", map[string]any{"domain_name": dom.Name})
			if err != nil {
				return fmt.Errorf("dns.dnssec_enable: %w", err)
			}
			// JAB-322 fail closed: only flip the DB flag on a well-formed,
			// ok=true reply. A malformed / ok=false reply leaves DNSSEC state
			// unchanged (the old `_ = json.Unmarshal` flipped it regardless).
			resp, ok := models.ParseDNSSECEnableReply(raw)
			if !ok {
				return fmt.Errorf("dns.dnssec_enable returned a malformed or unsuccessful reply; DNSSEC state left unchanged")
			}
			if err := domainRepoFromDB().UpdateDNSSECEnabled(ctx, dom.ID, true); err != nil {
				return fmt.Errorf("update dnssec flag: %w", err)
			}
			keysRepo := repository.NewDNSSECKeyRepository(sharedDB)
			now := time.Now().UTC()
			cached := make([]models.DomainDNSSECKey, 0, len(resp.Keys))
			for _, k := range resp.Keys {
				cached = append(cached, models.DomainDNSSECKey{
					DomainID:   dom.ID,
					KeyTag:     k.KeyTag,
					KeyType:    k.KeyType,
					Algorithm:  k.Algorithm,
					PublicKey:  k.PublicKey,
					Active:     k.Active,
					ObservedAt: now,
				})
			}
			// AC #2: a key-cache persist failure must be visible. The enable
			// itself succeeded (the zone is signed), so this warns rather than
			// failing the command; `status` re-lists from the cache and `get`
			// refreshes live, so the miss self-heals.
			if err := keysRepo.ReplaceAll(ctx, dom.ID, cached); err != nil {
				fmt.Fprintf(os.Stderr, "warning: DNSSEC enabled for %s but key cache not persisted: %v\n", dom.Name, err)
			}
			if jsonOutput {
				return printJSON(resp)
			}
			cliAuditOK(ctx, "dnssec.enable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("DNSSEC enabled for %s (%d keys)\n", dom.Name, len(resp.Keys))
			for _, k := range resp.Keys {
				fmt.Printf("  %s tag=%d alg=%d active=%t\n", k.KeyType, k.KeyTag, k.Algorithm, k.Active)
			}
			fmt.Println("Publish DS records at the parent registrar to activate the chain of trust:")
			fmt.Println("  jabali pdns dnssec ds " + dom.Name)
			return nil
		},
	}
}

func newPdnsDNSSECDisableCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "disable <domain>",
		Short:   "Disable DNSSEC (removes keys + rectifies)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				return fmt.Errorf("lookup domain %q: %w", args[0], err)
			}
			if !force {
				fmt.Printf("Disable DNSSEC for %s? Resolvers will validate as bogus until DS is removed at registrar. [y/N]: ", dom.Name)
				var c string
				fmt.Scanln(&c)
				if c != "y" && c != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			if _, err := sharedAgent.Call(ctx, "dns.dnssec_disable", map[string]any{"domain_name": dom.Name}); err != nil {
				return fmt.Errorf("dns.dnssec_disable: %w", err)
			}
			if err := domainRepoFromDB().UpdateDNSSECEnabled(ctx, dom.ID, false); err != nil {
				return fmt.Errorf("update dnssec flag: %w", err)
			}
			_ = repository.NewDNSSECKeyRepository(sharedDB).DeleteAllForDomain(ctx, dom.ID)
			if jsonOutput {
				return printJSON(map[string]any{"domain": dom.Name, "dnssec_enabled": false})
			}
			cliAuditOK(ctx, "dnssec.disable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("DNSSEC disabled for %s. Remove DS records at registrar to complete deactivation.\n", dom.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func newPdnsDNSSECDSCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ds <domain>",
		Short:   "Print DS records to publish at the parent registrar",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			// AC #3: DS export requires DNSSEC to be enabled for the zone.
			// Exporting DS for an unsigned zone would tempt an operator to
			// publish it at the registrar, which breaks resolution (resolvers
			// expect a signed zone). The lookup also validates the domain
			// exists before hitting the agent. Mirrors the HTTP dsExport 409.
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				return fmt.Errorf("lookup domain %q: %w", args[0], err)
			}
			if !dom.DNSSECEnabled {
				return fmt.Errorf("DNSSEC is not enabled for %s — run `jabali pdns dnssec enable %s` first; publishing DS for an unsigned zone breaks resolution", dom.Name, dom.Name)
			}
			raw, err := sharedAgent.Call(ctx, "dns.dnssec_ds_export", map[string]any{"domain_name": dom.Name})
			if err != nil {
				return fmt.Errorf("dns.dnssec_ds_export: %w", err)
			}
			var resp struct {
				DSRecords []struct {
					KeyTag     int    `json:"key_tag"`
					Algorithm  uint8  `json:"algorithm"`
					DigestType uint8  `json:"digest_type"`
					Digest     string `json:"digest"`
				} `json:"ds_records"`
			}
			_ = json.Unmarshal(raw, &resp)
			if jsonOutput {
				return printJSON(resp)
			}
			if len(resp.DSRecords) == 0 {
				fmt.Println("No DS records — DNSSEC may not be enabled.")
				return nil
			}
			fmt.Printf("DS records for %s:\n", args[0])
			for _, d := range resp.DSRecords {
				fmt.Printf("  %d %d %d %s\n", d.KeyTag, d.Algorithm, d.DigestType, d.Digest)
			}
			return nil
		},
	}
}

func newPdnsDNSSECStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status <domain>",
		Short:   "Show cached DNSSEC keys for a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				return fmt.Errorf("lookup domain %q: %w", args[0], err)
			}
			keys, err := repository.NewDNSSECKeyRepository(sharedDB).ListByDomainID(ctx, dom.ID)
			if err != nil {
				return fmt.Errorf("list keys: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain":         dom.Name,
					"dnssec_enabled": dom.DNSSECEnabled,
					"keys":           keys,
				})
			}
			fmt.Printf("Domain:   %s\n", dom.Name)
			fmt.Printf("Enabled:  %s\n", boolYN(dom.DNSSECEnabled))
			fmt.Printf("Keys:     %d\n", len(keys))
			for _, k := range keys {
				fmt.Printf("  %s tag=%d alg=%d active=%t observed=%s\n",
					k.KeyType, k.KeyTag, k.Algorithm, k.Active, k.ObservedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
}

type pdnsBackfillOpts struct {
	yes            bool
	verbose        bool
	noninteractive bool
}

func newPdnsBackfillCmd() *cobra.Command {
	var opts pdnsBackfillOpts
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Converge /etc/powerdns/recursor.forwards with the panel database",
		Long: `Walks enabled domains in the panel DB plus the panel's self-zone, and
compares against the recursor's current forward-zones-file state.

Default is --dry-run: prints the planned adds/removes/no-ops, exits 0,
makes no changes. Use --yes to actually apply via the panel-agent's
pdns.recursor_add_zone / pdns.recursor_remove_zone commands (idempotent
atomic writes with validator + rec_control reload + SOA post-probe +
rollback).

This CLI is the operator-driven converge path. The reconciler runs
the same operations every tick (default 60s), so in steady state
backfill's dry-run should report all 'noop'. When it doesn't, the
reconciler is either paused, broken, or behind — investigate before
applying --yes.`,
		PreRunE: requireDBAndAgent,
		RunE: func(_ *cobra.Command, _ []string) error {
			return opts.run(context.Background())
		},
	}
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "apply the plan (default is dry-run)")
	// --dry-run accepted for muscle-memory parity with other CLI verbs.
	// Mutually exclusive with --yes; absent both = default dry-run mode.
	var dryRunDecl bool
	cmd.Flags().BoolVar(&dryRunDecl, "dry-run", false, "explicit dry-run (default; mutually exclusive with --yes)")
	cmd.MarkFlagsMutuallyExclusive("yes", "dry-run")
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false, "print per-zone detail")
	cmd.Flags().BoolVar(&opts.noninteractive, "no-confirm", false,
		"skip the y/N confirmation when --yes is used (for scripted runs; "+
			"otherwise set JABALI_PDNS_BACKFILL_NONINTERACTIVE=1)")
	return cmd
}

// recursorAction describes one line of the plan table.
type recursorAction struct {
	Zone      string
	Forwarder string
	Action    string // "add" | "update" | "remove" | "noop"
}

// actualForwarder is the pure-function input shape for computeBackfillPlan
// — decoupled from the agent JSON schema so the unit test doesn't need
// to reach into the agent package.
type actualForwarder struct {
	Addr string
	Port int
}

const backfillForwarderAddr = "127.0.0.1"
const backfillForwarderPort = 5300

// computeBackfillPlan diffs the desired zone set (DB + self-zone) against
// the actual recursor.forwards state and returns a sorted plan. Pure
// function — tested in pdns_cmd_test.go.
func computeBackfillPlan(desired map[string]bool, actual map[string]actualForwarder) []recursorAction {
	var plan []recursorAction
	for zone := range desired {
		existing, ok := actual[zone]
		switch {
		case !ok:
			plan = append(plan, recursorAction{
				Zone:      zone,
				Forwarder: fmt.Sprintf("%s:%d", backfillForwarderAddr, backfillForwarderPort),
				Action:    "add",
			})
		case existing.Addr == backfillForwarderAddr && existing.Port == backfillForwarderPort:
			plan = append(plan, recursorAction{
				Zone:      zone,
				Forwarder: fmt.Sprintf("%s:%d", existing.Addr, existing.Port),
				Action:    "noop",
			})
		default:
			plan = append(plan, recursorAction{
				Zone:      zone,
				Forwarder: fmt.Sprintf("%s:%d", backfillForwarderAddr, backfillForwarderPort),
				Action:    "update",
			})
		}
	}
	for zone := range actual {
		if desired[zone] {
			continue
		}
		plan = append(plan, recursorAction{
			Zone:      zone,
			Forwarder: "—",
			Action:    "remove",
		})
	}
	sort.SliceStable(plan, func(i, j int) bool {
		if plan[i].Action != plan[j].Action {
			rank := map[string]int{"add": 0, "update": 1, "noop": 2, "remove": 3}
			return rank[plan[i].Action] < rank[plan[j].Action]
		}
		return plan[i].Zone < plan[j].Zone
	})
	return plan
}

func (o *pdnsBackfillOpts) run(ctx context.Context) error {
	// 1. Walk enabled domains from the DB.
	domRepo := repository.NewDomainRepository(sharedDB)
	desired := map[string]bool{}

	// Enumerate ALL domains (no pagination filter); a panel with thousands
	// of domains isn't the M6.3 scope, but add a visible cap so runaway
	// queries don't OOM.
	opts := repository.ListOptions{Limit: 10000}
	allDomains, _, err := domRepo.List(ctx, opts)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	for _, d := range allDomains {
		if d.IsEnabled {
			desired[d.Name] = true
		}
	}

	// 2. Add the self-zone.
	ssRepo := repository.NewServerSettingsRepository(sharedDB)
	if ss, sErr := ssRepo.Get(ctx); sErr == nil && ss != nil && ss.Hostname != "" {
		desired[ss.Hostname] = true
	} else if o.verbose {
		fmt.Fprintln(os.Stderr, "note: server settings hostname empty — skipping self-zone backfill")
	}

	// 3. Fetch current recursor.forwards via the agent.
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := sharedAgent.Call(listCtx, "pdns.recursor_list", nil)
	if err != nil {
		return fmt.Errorf("agent pdns.recursor_list: %w", err)
	}
	var listResp struct {
		Entries []struct {
			Zone string `json:"zone"`
			Addr string `json:"addr"`
			Port int    `json:"port"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		return fmt.Errorf("decode recursor_list: %w", err)
	}
	actual := map[string]struct {
		Addr string
		Port int
	}{}
	for _, e := range listResp.Entries {
		actual[e.Zone] = struct {
			Addr string
			Port int
		}{e.Addr, e.Port}
	}

	// 4. Compute plan.
	actualMap := make(map[string]actualForwarder, len(actual))
	for z, e := range actual {
		actualMap[z] = actualForwarder{Addr: e.Addr, Port: e.Port}
	}
	plan := computeBackfillPlan(desired, actualMap)

	// 5. Print.
	o.printPlan(plan)

	// 6. Dry-run path.
	if !o.yes {
		if countNonNoop(plan) > 0 {
			fmt.Fprintln(os.Stderr, "\n(dry-run; pass --yes to apply)")
		}
		return nil
	}

	// 7. Confirmation (--yes).
	if !o.noninteractive && os.Getenv("JABALI_PDNS_BACKFILL_NONINTERACTIVE") != "1" {
		n := countNonNoop(plan)
		if n == 0 {
			// Nothing to do; skip the prompt.
			return nil
		}
		fmt.Fprintf(os.Stderr, "\nApply %d change(s)? [y/N]: ", n)
		var resp string
		fmt.Scanln(&resp)
		if strings.ToLower(strings.TrimSpace(resp)) != "y" && strings.ToLower(strings.TrimSpace(resp)) != "yes" {
			fmt.Fprintln(os.Stderr, "aborted.")
			return fmt.Errorf("user did not confirm")
		}
	}

	// 8. Apply.
	return o.apply(ctx, plan)
}

func (o *pdnsBackfillOpts) printPlan(plan []recursorAction) {
	// Compute column widths.
	zoneW := len("ZONE")
	fwdW := len("FORWARDER")
	for _, p := range plan {
		if len(p.Zone) > zoneW {
			zoneW = len(p.Zone)
		}
		if len(p.Forwarder) > fwdW {
			fwdW = len(p.Forwarder)
		}
	}
	fmt.Printf("%-*s  %-*s  %s\n", zoneW, "ZONE", fwdW, "FORWARDER", "ACTION")
	fmt.Printf("%s  %s  %s\n", strings.Repeat("-", zoneW), strings.Repeat("-", fwdW), strings.Repeat("-", 6))
	for _, p := range plan {
		fmt.Printf("%-*s  %-*s  %s\n", zoneW, p.Zone, fwdW, p.Forwarder, p.Action)
	}
	if len(plan) == 0 {
		fmt.Println("(no zones — DB empty and recursor.forwards empty)")
	}
}

func (o *pdnsBackfillOpts) apply(ctx context.Context, plan []recursorAction) error {
	var errs []string
	for _, p := range plan {
		switch p.Action {
		case "add", "update":
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := sharedAgent.Call(cctx, "pdns.recursor_add_zone", map[string]any{
				"zone": p.Zone,
				"addr": backfillForwarderAddr,
				"port": backfillForwarderPort,
			})
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s add: %v", p.Zone, err))
				fmt.Fprintf(os.Stderr, "ERR  %s  add failed: %v\n", p.Zone, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "OK   %s  added\n", p.Zone)
		case "remove":
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := sharedAgent.Call(cctx, "pdns.recursor_remove_zone", map[string]any{
				"zone": p.Zone,
			})
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s remove: %v", p.Zone, err))
				fmt.Fprintf(os.Stderr, "ERR  %s  remove failed: %v\n", p.Zone, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "OK   %s  removed\n", p.Zone)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s) during apply:\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return nil
}

func countNonNoop(plan []recursorAction) int {
	n := 0
	for _, p := range plan {
		if p.Action != "noop" {
			n++
		}
	}
	return n
}

// Compile-time keep — ensures models package stays imported for the test
// file's type shape even if the production code eliminates the reference.
var _ = models.Domain{}
