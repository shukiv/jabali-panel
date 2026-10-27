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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
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
		Short: "AIDE file integrity monitor operator commands",
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
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			return runAideCheck(ctx, sharedAgent.Call, os.Stdout)
		},
	}
}

// How often `jabali aide check` asks whether the check ended, and how long it
// waits to see a just-started check running; vars for tests.
var (
	aideCheckPoll       = 5 * time.Second
	aideCheckStartGrace = 15 * time.Second
)

// runAideCheck starts a check through the agent and waits for it to end, then
// prints the status. GH #2079: security.aide.check starts
// jabali-aide-check.service and returns at once.
func runAideCheck(ctx context.Context, call func(context.Context, string, any) (json.RawMessage, error), out io.Writer) error {
	if _, err := call(ctx, "security.aide.check", map[string]any{}); err != nil {
		return fmt.Errorf("aide check: %w", err)
	}
	fmt.Fprintln(out, "aide check: running jabali-aide-check.service (2-5 min)…")
	graceEnd := time.Now().Add(aideCheckStartGrace)
	seenRunning := false
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("aide check: %w", ctx.Err())
		case <-time.After(aideCheckPoll):
		}
		raw, err := call(ctx, "security.aide.status", map[string]any{})
		if err != nil {
			return fmt.Errorf("aide status: %w", err)
		}
		var st struct {
			Check *struct {
				Running bool `json:"running"`
				Failed  bool `json:"failed"`
			} `json:"check"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return fmt.Errorf("aide status: %w", err)
		}
		if st.Check != nil && st.Check.Running {
			seenRunning = true
			continue
		}
		// The unit may not show as running yet right after the start.
		if !seenRunning && time.Now().Before(graceEnd) {
			continue
		}
		if st.Check != nil && st.Check.Failed {
			return errors.New("aide check failed; see journalctl -u jabali-aide-check.service")
		}
		_, _ = out.Write(append(raw, '\n'))
		return nil
	}
}

func newAideRebuildCmd() *cobra.Command {
	var (
		fullRebuild bool
		dryRun      bool
		pathsRegex  string
		resultFile  string
	)
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Re-baseline the AIDE database after a deliberate change",
		Long: `Re-baseline AIDE. Defaults to a full --init that rewrites
/var/lib/aide/aide.db. Use --full to make this explicit.

--dry-run reports the planned action (which DB file gets rewritten,
expected runtime, current DB size) without running aide. Same
default-safe behaviour as 'pdns backfill' and 'nspawn prune'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				fmt.Println("aide rebuild --dry-run:")
				fmt.Println("  would invoke: " + aideBinary + " --init --config=" + aideConfigPath + " (as root)")
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
			// GH #2079: the full rebuild runs aide --init as root. It used to
			// run Debian's aideinit, which runs aide as the _aide user; _aide
			// can't open jabali's report_url (/var/log/aide/aide.report.log,
			// written as root by the daily check), so aide exited 17 and the
			// reason went only to /var/log/aide/aideinit.errors.
			fmt.Println("aide rebuild: running 'aide --init' (2-5 min)…")
			started := time.Now().UTC()
			if resultFile != "" {
				if err := writeAideRebuildResult(resultFile, agentwire.AideRebuildResult{Running: true, StartedAt: started.Format(time.RFC3339)}); err != nil {
					return err
				}
			}
			_, rerr := rebuildAideBaseline(cmd.Context())
			if resultFile != "" {
				res := agentwire.AideRebuildResult{OK: rerr == nil, StartedAt: started.Format(time.RFC3339), FinishedAt: time.Now().UTC().Format(time.RFC3339)}
				if rerr != nil {
					res.Error = rerr.Error()
				}
				if err := writeAideRebuildResult(resultFile, res); err != nil && rerr == nil {
					return err
				}
			}
			if rerr != nil {
				return rerr
			}
			fmt.Println("aide rebuild: complete")
			return nil
		},
	}
	cmd.Flags().BoolVar(&fullRebuild, "full", false, "Confirm full DB re-init")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview plan without rebuilding (mutually exclusive with --full)")
	cmd.Flags().StringVar(&resultFile, "result-file", "", "Record the outcome of a --full rebuild in this file (used by the panel, which runs the rebuild in the background)")
	_ = cmd.Flags().MarkHidden("result-file")
	cmd.Flags().StringVar(&pathsRegex, "paths", "", "Partial re-baseline: only promote changes matching this regex (e.g. '^/usr/local/bin/jabali-'); refuses if changes outside the regex are detected")
	cmd.MarkFlagsMutuallyExclusive("full", "dry-run")
	cmd.MarkFlagsMutuallyExclusive("paths", "dry-run")
	cmd.MarkFlagsMutuallyExclusive("paths", "full")
	return cmd
}

// Paths of the full rebuild; vars so tests can point them at a temp dir.
var (
	aideBinary     = "/usr/bin/aide"
	aideConfigPath = "/etc/aide/aide.conf"
	aideDBFile     = "/var/lib/aide/aide.db"
	aideDBNewFile  = "/var/lib/aide/aide.db.new"
)

// rebuildAideBaseline re-initialises the AIDE database from the current
// filesystem as root, and puts the new database in place of the old one only
// when aide succeeded. AIDE 0.19 has no default config, so --config is always
// passed (as install.sh's first init does). On failure the error carries
// aide's own ERROR lines.
func rebuildAideBaseline(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, aideBinary, "--init", "--config="+aideConfigPath).CombinedOutput()
	if err != nil {
		_ = os.Remove(aideDBNewFile)
		if reason := aideErrorLines(string(out)); reason != "" {
			return string(out), fmt.Errorf("aide --init failed (%v): %s", err, reason)
		}
		return string(out), fmt.Errorf("aide --init failed: %w", err)
	}
	if err := os.Rename(aideDBNewFile, aideDBFile); err != nil {
		return string(out), fmt.Errorf("aide --init wrote no new database: %w", err)
	}
	_ = os.Chmod(aideDBFile, 0o600)
	return string(out), nil
}

// aideErrorLines returns aide's ERROR lines from its output, joined, or "".
func aideErrorLines(out string) string {
	var errs []string
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "ERROR:") {
			errs = append(errs, l)
		}
	}
	return strings.Join(errs, "; ")
}

// writeAideRebuildResult replaces path with r, atomically.
func writeAideRebuildResult(path string, r agentwire.AideRebuildResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}
