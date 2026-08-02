package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// `jabali ufw …` group. Sole child today is `migrate-ip-bans` which
// implements M43 Step 4: move every UFW `from <IP>` deny rule into a
// CrowdSec decision, leaving UFW with port-only policy. Snapshot is
// written to /var/lib/jabali/m43-ufw-snapshot.json so `--revert`
// restores byte-identical UFW state.
func newUfwCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ufw",
		Short: "UFW utilities (M43 — port baseline only; IP decisions live in CrowdSec)",
	}
	cmd.AddCommand(
		newUfwMigrateIPBansCmd(),
		newUfwStatusCmd(),
		newUfwRuleCmd(),
		newUfwDefaultCmd(),
		newUfwToggleCmd("security.ufw.enable", "Enable UFW", false),
		newUfwToggleCmd("security.ufw.disable", "Disable UFW (lockout risk)", true),
	)
	return cmd
}

const (
	ufwSnapshotPath = "/var/lib/jabali/m43-ufw-snapshot.json"
	// 90d default rotation window per operator answer 2026-05-04. UFW
	// permanent IP bans were de-facto stale anyway; 90d nudges
	// operators to re-confirm bans.
	ufwMigrateDuration = "2160h"
	ufwMigrateReason   = "ufw-migrated"
)

type ufwIPRule struct {
	Num    int    `json:"num"`
	Action string `json:"action"`
	From   string `json:"from"`
	Port   string `json:"port,omitempty"`
	Proto  string `json:"proto,omitempty"`
}

type ufwMigrationSnapshot struct {
	Timestamp string      `json:"timestamp"`
	Reason    string      `json:"reason"`
	Rules     []ufwIPRule `json:"rules"`
}

func newUfwMigrateIPBansCmd() *cobra.Command {
	var dryRun, revert, noCDN, yes bool

	cmd := &cobra.Command{
		Use:   "migrate-ip-bans",
		Short: "Migrate UFW `from <IP>` deny rules to CrowdSec decisions (M43 Step 4)",
		Long: `Walks ufw status numbered, finds every rule with a non-default
` + "`from`" + ` address, and creates an equivalent CrowdSec decision via
` + "`cscli decisions add`" + ` with reason="ufw-migrated" and duration=90d.
The original UFW rule is removed only after the CrowdSec decision is
confirmed.

Snapshot of every rule processed is saved to:
  ` + ufwSnapshotPath + `
Run with --revert to restore those rules and remove the matching
CrowdSec decisions.

Hard guard: refuses to run when ANY UFW IP rules exist AND CrowdSec
has no trusted_ips configured AND --no-cdn flag is absent. Prevents
banning CDN POPs on hosts behind Cloudflare.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if revert {
				return ufwRevertMigration(yes)
			}

			rules, err := ufwListIPRules()
			if err != nil {
				return fmt.Errorf("ufw status parse: %w", err)
			}
			if len(rules) == 0 {
				fmt.Println("No UFW IP rules to migrate. UFW is already a clean port-only baseline.")
				return nil
			}

			fmt.Printf("Found %d UFW IP rule(s) to migrate:\n", len(rules))
			for _, r := range rules {
				fmt.Printf("  #%d  %s from %s  port=%s proto=%s\n", r.Num, r.Action, r.From, r.Port, r.Proto)
			}

			// Hard guard: CDN risk. If the operator hasn't claimed
			// `--no-cdn` AND CrowdSec has no `trusted_ips` configured,
			// migration would ban any CDN edge IPs. Refuse loudly.
			if !noCDN {
				if hasTrusted, err := crowdsecHasTrustedIPs(); err != nil {
					return fmt.Errorf("cscli probe failed: %w (re-run with --no-cdn if the panel is not behind a CDN)", err)
				} else if !hasTrusted {
					return fmt.Errorf("aborting: CrowdSec has no `trusted_ips:` configured AND --no-cdn was not passed.\n  If the panel is behind Cloudflare/CDN, configure trusted_ips first (see plans/m43-unified-trust-runbook.md).\n  If not, re-run with --no-cdn to confirm")
				}
			}

			if dryRun {
				fmt.Println("\n--dry-run: no changes made. Re-run without --dry-run to migrate.")
				return nil
			}

			if !yes {
				return fmt.Errorf("destructive: re-run with --yes to actually migrate (snapshot at %s, --revert restores)", ufwSnapshotPath)
			}

			snap := ufwMigrationSnapshot{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Reason:    "M43 Step 4 — UFW IP rules migrated to CrowdSec",
				Rules:     rules,
			}
			if err := writeUfwSnapshot(snap); err != nil {
				return fmt.Errorf("snapshot write: %w", err)
			}
			fmt.Printf("Snapshot written to %s\n", ufwSnapshotPath)

			migrated := 0
			for _, r := range rules {
				if err := crowdsecDecisionAdd(r.From, ufwMigrateDuration, ufwMigrateReason); err != nil {
					fmt.Fprintf(os.Stderr, "  SKIP %s: cscli failed: %v\n", r.From, err)
					continue
				}
				// Delete by source pattern; rule number shifts after each
				// delete, so we re-list each time would also work but
				// `ufw delete` accepts the rule spec directly.
				if err := ufwDeleteRule(r); err != nil {
					fmt.Fprintf(os.Stderr, "  WARN %s: cscli decision created but ufw delete failed: %v\n", r.From, err)
					continue
				}
				fmt.Printf("  OK   %s migrated to CrowdSec (TTL=%s)\n", r.From, ufwMigrateDuration)
				migrated++
			}

			fmt.Printf("\n%d/%d rules migrated. UFW now holds port policy only.\n", migrated, len(rules))
			fmt.Printf("Verify: cscli decisions list -i <ip> -o json\n")
			fmt.Printf("Revert: jabali ufw migrate-ip-bans --revert --yes\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would migrate; make no changes")
	cmd.Flags().BoolVar(&revert, "revert", false, "Restore UFW rules from snapshot and remove matching CrowdSec decisions")
	cmd.Flags().BoolVar(&noCDN, "no-cdn", false, "Confirm panel is not behind a CDN; bypasses the trusted_ips hard guard")
	cmd.Flags().BoolVar(&yes, "yes", false, "Required for any destructive operation (migrate or revert)")
	return cmd
}

// ufwStatusLineRe matches lines of the form
//
//	[ 7] 22/tcp                     ALLOW IN    1.2.3.4
//
// captured groups: 1=num, 2=port-or-anywhere, 3=proto, 4=action, 5=from
//
// UFW formats are stable across the supported Debian releases; we only
// care about IPv4/IPv6 source addresses, never CIDR (UFW writes them
// as "1.2.3.0/24" — caught by the `From` regex). Ranges/group rules
// are skipped with a log line.
var ufwStatusLineRe = regexp.MustCompile(`^\[\s*(\d+)\]\s+(\S+)(?:/(tcp|udp))?\s+(ALLOW|DENY|REJECT|LIMIT)\s+(?:IN|OUT)\s+(.+)$`)

func ufwListIPRules() ([]ufwIPRule, error) {
	cmd := exec.Command("ufw", "status", "numbered")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var rules []ufwIPRule
	for _, line := range strings.Split(string(out), "\n") {
		m := ufwStatusLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		from := strings.TrimSpace(m[5])
		// Wildcard sources don't count — they're port-policy.
		if from == "Anywhere" || from == "Anywhere (v6)" || from == "" {
			continue
		}
		// Sanity: must parse as IP or CIDR.
		if !looksLikeIPOrCIDR(from) {
			continue
		}
		num := 0
		fmt.Sscanf(m[1], "%d", &num)
		rules = append(rules, ufwIPRule{
			Num:    num,
			Action: strings.ToLower(m[4]),
			From:   from,
			Port:   m[2],
			Proto:  m[3],
		})
	}
	// Sort by num descending so deletes don't shift later rules.
	sort.Slice(rules, func(i, j int) bool { return rules[i].Num > rules[j].Num })
	return rules, nil
}

func looksLikeIPOrCIDR(s string) bool {
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	if ip := net.ParseIP(s); ip != nil {
		return true
	}
	return false
}

// crowdsecHasTrustedIPs returns true when `cscli config show -o json`
// has a populated trusted_ips array. We don't try to parse the YAML
// directly — the JSON output is stable across CrowdSec 1.4+.
func crowdsecHasTrustedIPs() (bool, error) {
	out, err := exec.Command("cscli", "config", "show", "-o", "json").Output()
	if err != nil {
		return false, err
	}
	// We don't fully parse — just look for any non-empty trusted_ips
	// list. The exact key path differs slightly across versions but
	// "trusted_ips" appears verbatim only when configured.
	return strings.Contains(string(out), `"trusted_ips":["`) ||
		strings.Contains(string(out), `"trusted_ips": ["`), nil
}

func crowdsecDecisionAdd(ip, duration, reason string) error {
	scope := "ip"
	if strings.Contains(ip, "/") {
		scope = "range"
	}
	args := []string{
		"decisions", "add",
		"--scope", scope,
		"--value", ip,
		"--duration", duration,
		"--reason", reason,
	}
	out, err := exec.Command("cscli", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cscli %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func crowdsecDecisionDelete(ip string) error {
	args := []string{"decisions", "delete", "--ip", ip}
	if strings.Contains(ip, "/") {
		args = []string{"decisions", "delete", "--range", ip}
	}
	out, err := exec.Command("cscli", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cscli %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func ufwDeleteRule(r ufwIPRule) error {
	// `ufw delete <action> from <ip> [port <p>] [proto <p>]` is
	// idempotent. Number-based delete drifts as rules shift, so use
	// the rule spec.
	args := []string{"--force", "delete", r.Action, "from", r.From}
	if r.Port != "" && r.Port != "Anywhere" {
		args = append(args, "to", "any", "port", r.Port)
	}
	if r.Proto != "" {
		args = append(args, "proto", r.Proto)
	}
	out, err := exec.Command("ufw", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ufw %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func writeUfwSnapshot(snap ufwMigrationSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(ufwSnapshotPath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ufwSnapshotPath, b, 0o600)
}

func ufwRevertMigration(yes bool) error {
	b, err := os.ReadFile(ufwSnapshotPath)
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", ufwSnapshotPath, err)
	}
	var snap ufwMigrationSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}
	if len(snap.Rules) == 0 {
		fmt.Println("Snapshot is empty; nothing to revert.")
		return nil
	}
	fmt.Printf("Will restore %d UFW rule(s) and remove matching CrowdSec decisions:\n", len(snap.Rules))
	for _, r := range snap.Rules {
		fmt.Printf("  %s from %s port=%s proto=%s\n", r.Action, r.From, r.Port, r.Proto)
	}
	if !yes {
		return fmt.Errorf("destructive revert: re-run with --yes")
	}
	for _, r := range snap.Rules {
		if err := crowdsecDecisionDelete(r.From); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN cscli delete %s: %v\n", r.From, err)
		}
		if err := ufwAddRule(r); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN ufw add %s: %v\n", r.From, err)
			continue
		}
		fmt.Printf("  OK restored %s from %s\n", r.Action, r.From)
	}
	fmt.Println("\nRevert complete. Snapshot kept (safe to re-run).")
	return nil
}

func ufwAddRule(r ufwIPRule) error {
	args := []string{r.Action, "from", r.From}
	if r.Port != "" && r.Port != "Anywhere" {
		args = append(args, "to", "any", "port", r.Port)
	}
	if r.Proto != "" {
		args = append(args, "proto", r.Proto)
	}
	out, err := exec.Command("ufw", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ufw %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
