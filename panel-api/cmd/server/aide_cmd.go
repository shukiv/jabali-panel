// `jabali aide {status,rebuild}` — operator CLI for AIDE FIM.
//
//   jabali aide status                — print DB age + last check summary
//   jabali aide rebuild               — re-init AIDE DB from current FS state
//                                       (run after a deliberate kernel/binary
//                                       upgrade so daily check stays clean)
//   jabali aide rebuild --paths PATTERN
//                                     — partial re-baseline of matching paths
//                                       only (use after `jabali update` to
//                                       refresh /usr/local/bin/jabali-* binary
//                                       checksums without nuking the whole DB).
//
// See ADR-0087 + plans/m42-aide-fim-system-integrity.md.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// parseAideDiffPaths scans aide --update / aide --check report
// output for path tokens. AIDE's canonical line shapes:
//
//	added: /usr/local/bin/jabali-foo
//	removed: /etc/foo.conf.old
//	changed: /usr/local/bin/jabali-bar
//	File: /usr/local/bin/jabali-baz
//	f++++++++ /usr/local/bin/jabali-qux  (older AIDE)
//
// We extract the absolute path + dedup. Caller's regex match
// runs against this slice.
func parseAideDiffPaths(report string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		var path string
		switch {
		case strings.HasPrefix(line, "added: "):
			path = strings.TrimPrefix(line, "added: ")
		case strings.HasPrefix(line, "removed: "):
			path = strings.TrimPrefix(line, "removed: ")
		case strings.HasPrefix(line, "changed: "):
			path = strings.TrimPrefix(line, "changed: ")
		case strings.HasPrefix(line, "File: "):
			path = strings.TrimPrefix(line, "File: ")
		case strings.HasPrefix(line, "f++++++++"):
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				path = parts[1]
			}
		default:
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func newAideCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aide",
		Short: "AIDE file integrity monitor (M42) operator commands",
	}
	cmd.AddCommand(newAideStatusCmd())
	cmd.AddCommand(newAideCheckCmd())
	cmd.AddCommand(newAideRebuildCmd())
	return cmd
}

func newAideStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print AIDE DB age + last check summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, statErr := os.Stat("/var/lib/aide/aide.db")
			reportExists := false
			if _, rerr := os.Stat("/var/log/aide/aide.report.log"); rerr == nil {
				reportExists = true
			}
			if jsonOutput {
				out := map[string]any{
					"db_present":     statErr == nil,
					"db_path":        "/var/lib/aide/aide.db",
					"report_present": reportExists,
				}
				if statErr == nil {
					out["db_mtime"] = st.ModTime().UTC().Format(time.RFC3339)
				}
				return printJSON(out)
			}
			if statErr != nil {
				fmt.Println("aide: DB missing — run 'jabali aide rebuild --full'")
				return nil
			}
			fmt.Printf("aide: DB at %s (mtime %s)\n", "/var/lib/aide/aide.db", st.ModTime().Format("2006-01-02 15:04 UTC"))
			if reportExists {
				out, err := exec.Command("tail", "-30", "/var/log/aide/aide.report.log").Output()
				if err == nil {
					fmt.Println("---- last report (tail 30) ----")
					fmt.Print(string(out))
				}
			}
			return nil
		},
	}
}

// newAideCheckCmd triggers an on-demand AIDE check via the agent (same verb as
// the GUI). The agent reports whether the check started, was already running,
// or failed validation; the CLI surfaces that and exits non-zero on error.
func newAideCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "check",
		Short:   "Trigger an on-demand AIDE integrity check",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, "security.aide.check", map[string]any{})
			if err != nil {
				return fmt.Errorf("aide check: %w", err)
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newAideRebuildCmd() *cobra.Command {
	var (
		fullRebuild bool
		dryRun      bool
		pathsRegex  string
	)
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Re-baseline the AIDE database after a deliberate change",
		Long: `Re-baseline AIDE. Defaults to a full --init that rewrites
/var/lib/aide/aide.db. Use --full to make this explicit.

--dry-run reports the planned action (which DB file gets rewritten,
expected runtime, current DB size) without invoking aideinit. Same
default-safe behaviour as 'pdns backfill' and 'nspawn prune'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				fmt.Println("aide rebuild --dry-run:")
				fmt.Println("  would invoke: /usr/sbin/aideinit -y -f")
				fmt.Println("  target db   : /var/lib/aide/aide.db")
				if st, err := os.Stat("/var/lib/aide/aide.db"); err == nil {
					fmt.Printf("  current db  : %s, mtime %s, %d bytes\n",
						"/var/lib/aide/aide.db", st.ModTime().Format("2006-01-02 15:04 UTC"), st.Size())
				} else {
					fmt.Println("  current db  : missing (first-time init)")
				}
				fmt.Println("  est. runtime: 2-5 min on a typical host")
				fmt.Println("re-run with --full to actually rebuild")
				return nil
			}
			// Partial path-scoped re-baseline. Runs aide --update
			// (writes aide.db.new from current FS state), parses
			// the diff report, and only promotes db.new -> db when
			// every reported change path matches --paths regex.
			// Non-matching diffs (a binary changed outside the
			// expected /usr/local/bin/jabali-* set) abort promotion
			// + print the diff so the operator inspects.
			if pathsRegex != "" {
				re, err := regexp.Compile(pathsRegex)
				if err != nil {
					return fmt.Errorf("invalid --paths regex: %w", err)
				}
				fmt.Println("aide rebuild --paths: running 'aide --update' (1-3 min)...")
				updOut, updErr := exec.Command("aide", "--update", "--config", "/etc/aide/aide.conf").CombinedOutput()
				// aide --update returns non-zero exit when diffs
				// exist (which is normal for a re-baseline). We
				// still need the report; only fail on
				// missing-binary / config-error cases.
				if updErr != nil && len(updOut) == 0 {
					return fmt.Errorf("aide --update: %w", updErr)
				}
				if _, err := os.Stat("/var/lib/aide/aide.db.new"); err != nil {
					return fmt.Errorf("aide --update produced no db.new: %s", string(updOut))
				}
				report, _ := os.ReadFile("/var/log/aide/aide.report.log")
				diffPaths := parseAideDiffPaths(string(report) + "\n" + string(updOut))
				var unexpected []string
				for _, p := range diffPaths {
					if !re.MatchString(p) {
						unexpected = append(unexpected, p)
					}
				}
				if len(unexpected) > 0 {
					fmt.Println("aide rebuild --paths: refusing — diffs outside the requested paths:")
					for _, p := range unexpected {
						fmt.Printf("  %s\n", p)
					}
					fmt.Println("inspect /var/log/aide/aide.report.log and either widen --paths or run 'aide rebuild --full'")
					_ = os.Remove("/var/lib/aide/aide.db.new")
					return fmt.Errorf("partial rebuild aborted (%d unexpected diffs)", len(unexpected))
				}
				if err := os.Rename("/var/lib/aide/aide.db.new", "/var/lib/aide/aide.db"); err != nil {
					return fmt.Errorf("rename aide.db.new: %w", err)
				}
				_ = os.Chmod("/var/lib/aide/aide.db", 0o600)
				fmt.Printf("aide rebuild --paths: %d path(s) re-baselined matching %q\n", len(diffPaths), pathsRegex)
				return nil
			}

			if !fullRebuild {
				fmt.Println("aide rebuild: pass --full to confirm full re-init (or --paths REGEX for partial, or --dry-run to preview)")
				return nil
			}
			// AIDE 0.19 requires an explicit --config or a compiled-in
			// default that Debian's aide package leaves unset. Calling
			// `aide --init` directly fails with exit 17 "missing
			// configuration". /usr/sbin/aideinit (from aide-common) is
			// the Debian-canonical wrapper: assembles /etc/aide/aide.conf
			// from the conf.d snippets, runs aide --init --config <that>,
			// and atomically renames aide.db.new -> aide.db. Same
			// approach install.sh uses for the first-boot init.
			fmt.Println("aide rebuild: running 'aideinit -y -f' (2-5 min)…")
			out, err := exec.Command("/usr/sbin/aideinit", "-y", "-f").CombinedOutput()
			fmt.Print(string(out))
			if err != nil {
				return fmt.Errorf("aideinit: %w", err)
			}
			// aideinit handles the rename internally; this fallback is
			// kept for the rare host where aideinit was patched to skip
			// it (no upstream Debian flavor does today).
			if _, err := os.Stat("/var/lib/aide/aide.db.new"); err == nil {
				if err := os.Rename("/var/lib/aide/aide.db.new", "/var/lib/aide/aide.db"); err != nil {
					return fmt.Errorf("rename aide.db.new: %w", err)
				}
				_ = os.Chmod("/var/lib/aide/aide.db", 0o600)
			}
			fmt.Println("aide rebuild: complete")
			return nil
		},
	}
	cmd.Flags().BoolVar(&fullRebuild, "full", false, "Confirm full DB re-init")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview plan without rebuilding (mutually exclusive with --full)")
	cmd.Flags().StringVar(&pathsRegex, "paths", "", "Partial re-baseline: only promote changes matching this regex (e.g. '^/usr/local/bin/jabali-'); refuses if changes outside the regex are detected")
	cmd.MarkFlagsMutuallyExclusive("full", "dry-run")
	cmd.MarkFlagsMutuallyExclusive("paths", "dry-run")
	cmd.MarkFlagsMutuallyExclusive("paths", "full")
	return cmd
}
