// `jabali apparmor flip-mature` — operator-driven flip of jabali
// AppArmor profiles from complain to enforce. For each complain-mode
// jabali profile it scans the kernel log for AppArmor DENIED events in
// the last --soak-days (default 7) and flips ONLY profiles with zero
// denials (a clean soak). A profile that is still denying is skipped
// with the pending denials surfaced — flipping it would EACCES the
// daemon's real syscalls and crash-loop it (JAB-17 / GH #705, which is
// exactly how mx went down). --force overrides the gate; --profile
// targets one.
//
// See ADR-0086 + plans/m40-apparmor-jabali-daemons.md Step 5.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var apparmorAllowlist = []string{
	"jabali-panel",
	"jabali-agent",
	"jabali-bulwark",
	"jabali-kratos",
	"stalwart-mail",
}

// apparmorProfileFile maps a profile name to the on-disk profile file
// shipped under /etc/apparmor.d/. aa-enforce/aa-complain accept either
// a binary path (resolved via PATH) or a profile-file path; profile
// names like "jabali-bulwark" don't resolve via PATH on Debian, so we
// always pass the file path explicitly.
func apparmorProfileFile(name string) string {
	switch name {
	case "jabali-panel":
		return "/etc/apparmor.d/usr.local.bin.jabali-panel-api"
	case "jabali-agent":
		return "/etc/apparmor.d/usr.local.bin.jabali-agent"
	case "jabali-bulwark":
		return "/etc/apparmor.d/usr.local.bin.jabali-bulwark"
	case "jabali-kratos":
		return "/etc/apparmor.d/usr.local.bin.jabali-kratos"
	case "stalwart-mail":
		return "/etc/apparmor.d/usr.local.bin.stalwart-mail"
	}
	return ""
}

func newAppArmorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apparmor",
		Short: "AppArmor profile management (M40) operator commands",
	}
	cmd.AddCommand(newAppArmorFlipMatureCmd())
	cmd.AddCommand(newAppArmorStatusCmd())
	return cmd
}

func newAppArmorStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List jabali AppArmor profiles and modes",
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := listJabaliApparmorProfiles()
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				fmt.Println("apparmor: no jabali profiles loaded")
				return nil
			}
			for name, mode := range profiles {
				fmt.Printf("  %-22s  %s\n", name, mode)
			}
			return nil
		},
	}
}

func newAppArmorFlipMatureCmd() *cobra.Command {
	var (
		profileFilter string
		dryRun        bool
		soakDays      int
		force         bool
	)
	cmd := &cobra.Command{
		Use:     "flip-mature",
		Short:   "Flip soak-clean complain-mode profiles to enforce",
		PreRunE: requireAgent,
		Long: `Find AppArmor profiles in complain mode and flip them to enforce,
but only when they are soak-clean: zero kernel AppArmor DENIED events in
the last --soak-days (default 7). A still-denying profile is skipped and
its pending denials are shown — flipping it would EACCES the daemon and
crash-loop it (JAB-17 / GH #705). Add the missing allow rules, keep
soaking, and re-run. --force flips despite denials (DANGEROUS).
Limits the flip to the jabali-shipped allowlist; never touches
arbitrary system profiles. Use --profile <name> to target one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := listJabaliApparmorProfiles()
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				fmt.Println("apparmor flip-mature: no jabali profiles loaded")
				return nil
			}

			toFlip := []string{}
			for _, name := range apparmorAllowlist {
				if profileFilter != "" && profileFilter != name {
					continue
				}
				mode, ok := profiles[name]
				if !ok {
					continue
				}
				if mode == "complain" {
					toFlip = append(toFlip, name)
				}
			}

			if len(toFlip) == 0 {
				fmt.Println("apparmor flip-mature: no complain-mode jabali profiles")
				return nil
			}

			fmt.Printf("apparmor flip-mature: %d complain-mode candidate(s); soak window %dd\n", len(toFlip), soakDays)
			flipped, skipped := 0, 0
			for _, name := range toFlip {
				// JAB-17 / GH #705 gate: never flip a profile that is still
				// denying. A DENIED in complain is a missing allow rule that
				// becomes a hard EACCES (and a daemon crash-loop) under enforce.
				denials, sample, derr := apparmorRecentDenials(name, soakDays)
				if derr != nil && !force {
					fmt.Fprintf(os.Stderr, "  %s: SKIP — could not verify soak-cleanliness (%v); --force to override\n", name, derr)
					skipped++
					continue
				}
				if denials > 0 && !force {
					fmt.Printf("  %s: SKIP — %d AppArmor denial(s) in the last %dd (not soak-clean)\n", name, denials, soakDays)
					if sample != "" {
						fmt.Printf("      e.g. %s\n", sample)
					}
					fmt.Printf("      Add the missing allow rule(s) to the profile and keep soaking; flipping now risks the #705 crash-loop.\n")
					skipped++
					continue
				}
				if denials > 0 && force {
					fmt.Printf("  %s: %d denial(s) in the last %dd — flipping anyway (--force)\n", name, denials, soakDays)
				}
				fmt.Printf("  %s  complain → enforce\n", name)
				if dryRun {
					continue
				}
				// GH #682: delegate the flip to panel-agent (which holds
				// capability mac_admin) instead of running aa-enforce here, so the
				// panel daemon profile no longer needs mac_admin.
				sctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				_, err := sharedAgent.Call(sctx, "security.apparmor.set_mode", map[string]any{"profile": name, "mode": "enforce"})
				cancel()
				if err != nil {
					fmt.Fprintf(os.Stderr, "  flip %s failed: %v\n", name, err)
					continue
				}
				flipped++
			}
			if dryRun {
				fmt.Printf("apparmor flip-mature: dry-run, no profiles flipped (%d would flip, %d skipped as not soak-clean)\n", len(toFlip)-skipped, skipped)
			} else {
				fmt.Printf("apparmor flip-mature: flipped %d profile(s) to enforce, %d skipped\n", flipped, skipped)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profileFilter, "profile", "", "Flip a single profile only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without invoking aa-enforce")
	cmd.Flags().IntVar(&soakDays, "soak-days", 7, "denial-scan window in days; a profile with any AppArmor DENIED in this window is not flipped")
	cmd.Flags().BoolVar(&force, "force", false, "flip even if recent denials exist (DANGEROUS — this is how #705 crash-looped mx)")
	return cmd
}

// listJabaliApparmorProfiles parses aa-status --json and returns the
// jabali-* + jabali-shipped profiles with their current mode.
func listJabaliApparmorProfiles() (map[string]string, error) {
	out, err := exec.Command("aa-status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("aa-status: %w", err)
	}
	var raw struct {
		Profiles map[string]string `json:"profiles"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse aa-status: %w", err)
	}
	out2 := map[string]string{}
	for name, mode := range raw.Profiles {
		if !strings.HasPrefix(name, "jabali-") && name != "stalwart-mail" {
			continue
		}
		out2[name] = mode
	}
	return out2, nil
}

// apparmorRecentDenials counts kernel AppArmor "DENIED" events for the given
// profile within the last `days` days (JAB-17). A non-zero count means the
// profile is NOT soak-clean: flipping it to enforce would turn those logged-
// but-permitted denials into hard EACCES and crash-loop the daemon (GH #705).
// Returns the count + one sample line. Fails closed (returns the error) rather
// than reporting a false "clean" when journalctl can't be read.
func apparmorRecentDenials(profile string, days int) (int, string, error) {
	args := []string{"-k", "--no-pager"}
	if days > 0 {
		args = append(args, "--since", fmt.Sprintf("%d days ago", days))
	} else {
		// A non-positive window would scan ~nothing via --since; fall back to
		// the whole current boot so the gate can't be trivially bypassed.
		args = append(args, "-b")
	}
	out, err := exec.Command("journalctl", args...).Output()
	if err != nil {
		return 0, "", fmt.Errorf("journalctl -k: %w", err)
	}
	count, sample := countApparmorDenials(string(out), profile)
	return count, sample, nil
}

// countApparmorDenials scans kernel-log text for AppArmor DENIED lines naming
// the given profile and returns the count + the first matching line. Split out
// from apparmorRecentDenials so the match logic is unit-testable without a
// live journal. A denial line looks like:
//
//	... apparmor="DENIED" operation="open" profile="jabali-panel" name="/x" ...
func countApparmorDenials(logText, profile string) (int, string) {
	needle := `profile="` + profile + `"`
	count := 0
	var sample string
	for _, line := range strings.Split(logText, "\n") {
		if strings.Contains(line, `apparmor="DENIED"`) && strings.Contains(line, needle) {
			count++
			if sample == "" {
				sample = strings.TrimSpace(line)
			}
		}
	}
	return count, sample
}
