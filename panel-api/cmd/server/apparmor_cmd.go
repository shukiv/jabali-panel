// `jabali apparmor flip-mature` — operator-driven flip of jabali
// AppArmor profiles from complain to enforce. For each complain-mode
// jabali profile it scans the audit log and the journal for AppArmor
// violations in the last --soak-days (default 7) — DENIED events, and the
// ALLOWED events a complain-mode profile logs for every access it would
// deny — and flips ONLY profiles with none (a clean soak). A profile that
// is still violating is skipped with the pending events surfaced — flipping
// it would EACCES the daemon's real syscalls and crash-loop it (JAB-17 /
// GH #705, which is exactly how mx went down). --force overrides the gate;
// --profile targets one.
//
// See ADR-0086 + plans/m40-apparmor-jabali-daemons.md Step 5.

package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
but only when they are soak-clean: no AppArmor violation in the last
--soak-days (default 7). A violation is a DENIED event, or an ALLOWED
event — what a complain-mode profile logs for each access it would deny.
Events are read from the audit log (/var/log/audit/audit.log and its
rotations) and from the journal. A still-violating profile is skipped and
its pending events are shown — flipping it would EACCES the daemon and
crash-loop it (JAB-17 / GH #705). Add the missing allow rules, keep
soaking, and re-run. --force flips despite violations (DANGEROUS).
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
				// violating. An ALLOWED in complain (or a DENIED) is a missing
				// allow rule that becomes a hard EACCES (and a daemon crash-loop)
				// under enforce.
				violations, sample, derr := apparmorRecentViolations(name, soakDays)
				if derr != nil && !force {
					fmt.Fprintf(os.Stderr, "  %s: SKIP — could not verify soak-cleanliness (%v); --force to override\n", name, derr)
					skipped++
					continue
				}
				if violations > 0 && !force {
					fmt.Printf("  %s: SKIP — %d AppArmor violation(s) in the last %dd (not soak-clean)\n", name, violations, soakDays)
					if sample != "" {
						fmt.Printf("      e.g. %s\n", sample)
					}
					fmt.Printf("      Add the missing allow rule(s) to the profile and keep soaking; flipping now risks the #705 crash-loop.\n")
					skipped++
					continue
				}
				if violations > 0 && force {
					fmt.Printf("  %s: %d violation(s) in the last %dd — flipping anyway (--force)\n", name, violations, soakDays)
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
	cmd.Flags().IntVar(&soakDays, "soak-days", 7, "violation-scan window in days; a profile with any AppArmor DENIED or complain-mode ALLOWED event in this window is not flipped")
	cmd.Flags().BoolVar(&force, "force", false, "flip even if recent violations exist (DANGEROUS — this is how #705 crash-looped mx)")
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

// apparmorAuditLogGlob matches auditd's log and its rotations. With auditd
// running (jabali installs it, ADR-0085), the kernel hands AppArmor records
// to the audit subsystem: they land in these files and never reach the
// kernel journal, so a journal-only scan finds nothing on a jabali box.
// Variable so tests can point it at a temp dir.
var apparmorAuditLogGlob = "/var/log/audit/audit.log*"

// apparmorRecentViolations counts the AppArmor violations of the given profile
// within the last `days` days (JAB-17): DENIED events, and the ALLOWED events
// a complain-mode profile logs for every access it would deny. A non-zero
// count means the profile is NOT soak-clean: flipping it to enforce would turn
// those accesses into hard EACCES and crash-loop the daemon (GH #705).
//
// It reads the audit log files and the journal's kernel and audit records; an
// event in both counts once. Returns the count + one sample line. Fails closed
// (returns the error) rather than reporting a false "clean" when journalctl or
// an audit log file can't be read.
func apparmorRecentViolations(profile string, days int) (int, string, error) {
	args := []string{"--no-pager", "-q"}
	var since time.Time // zero: every audit log record counts
	if days > 0 {
		since = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		args = append(args, "--since", fmt.Sprintf("%d days ago", days))
	} else {
		// A non-positive window would scan ~nothing via --since; fall back to
		// the whole current boot so the gate can't be trivially bypassed.
		args = append(args, "-b")
	}
	args = append(args, "_TRANSPORT=kernel", "+", "_TRANSPORT=audit")
	out, err := exec.Command("journalctl", args...).Output()
	if err != nil {
		return 0, "", fmt.Errorf("journalctl: %w", err)
	}
	seen := map[string]bool{}
	count, sample, err := countApparmorViolations(strings.NewReader(string(out)), profile, time.Time{}, seen)
	if err != nil {
		return 0, "", fmt.Errorf("journalctl: %w", err)
	}

	files, err := filepath.Glob(apparmorAuditLogGlob)
	if err != nil {
		return 0, "", fmt.Errorf("audit log glob: %w", err)
	}
	for _, path := range files {
		n, s, err := countApparmorViolationsInFile(path, profile, since, seen)
		if err != nil {
			return 0, "", err
		}
		count += n
		if sample == "" {
			sample = s
		}
	}
	return count, sample, nil
}

// countApparmorViolationsInFile counts the profile's violations in one audit
// log file; a logrotate-compressed rotation (.gz) is read through gzip.
func countApparmorViolationsInFile(path, profile string, since time.Time, seen map[string]bool) (int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, "", fmt.Errorf("read %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}
	n, s, err := countApparmorViolations(r, profile, since, seen)
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", path, err)
	}
	return n, s, nil
}

// apparmorAuditStampRe matches an audit record's stamp, audit(<epoch>.<ms>:<serial>),
// which is unique per record: the same event in the journal and in audit.log
// carries the same stamp.
var apparmorAuditStampRe = regexp.MustCompile(`audit\((\d+)\.\d+:\d+\)`)

// countApparmorViolations scans log text for AppArmor DENIED and ALLOWED lines
// naming the given profile and returns the count + the first matching line.
// Lines stamped before `since` are skipped (a zero `since` skips none), and a
// stamp already in `seen` is not counted again. A read error is returned: a
// scan cut short must not pass for a clean soak. Split out so the match logic
// is unit-testable without a live journal. Violation lines look like:
//
//	... audit(1792370395.123:456): apparmor="DENIED" operation="open" profile="jabali-panel" name="/x" ...
//	... audit(1792370395.123:457): apparmor="ALLOWED" operation="open" profile="jabali-panel" name="/x" ...
func countApparmorViolations(r io.Reader, profile string, since time.Time, seen map[string]bool) (int, string, error) {
	needle := `profile="` + profile + `"`
	count := 0
	var sample string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, needle) ||
			!(strings.Contains(line, `apparmor="DENIED"`) || strings.Contains(line, `apparmor="ALLOWED"`)) {
			continue
		}
		if m := apparmorAuditStampRe.FindStringSubmatch(line); m != nil {
			if !since.IsZero() {
				if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil && time.Unix(sec, 0).Before(since) {
					continue
				}
			}
			if seen[m[0]] {
				continue
			}
			seen[m[0]] = true
		}
		count++
		if sample == "" {
			// auditd's ENRICHED format appends interpreted fields after a
			// 0x1d separator; the raw record before it is the useful part.
			raw, _, _ := strings.Cut(line, "\x1d")
			sample = strings.TrimSpace(raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, "", err
	}
	return count, sample, nil
}
