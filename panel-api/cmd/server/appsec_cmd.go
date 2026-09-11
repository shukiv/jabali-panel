package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newAppSecCmd is the operator-facing parent for AppSec config ops
// (ADR-0102 / ADR-0083 single-source). Sub: `render-config`.
func newAppSecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "appsec",
		Short: "CrowdSec AppSec config operator subcommands",
	}
	cmd.AddCommand(newAppSecRenderConfigCmd())
	cmd.AddCommand(newAppSecExplainCmd())
	cmd.AddCommand(newAppSecExclusionCmd())
	return cmd
}

const (
	appsecConfigPath    = "/etc/crowdsec/appsec-configs/jabali-appsec.yaml"
	appsecRulesDir      = "/etc/crowdsec/appsec-rules"
	appsecModeHeader    = "# jabali-mode:"
	appsecCountriesHead = "# jabali-countries:"
	appsecBotDetectHead = "# jabali-bot-detection:"
	appsecBotScopeHead  = "# jabali-bot-detection-scope:"
)

// newAppSecRenderConfigCmd is the canonical writer of
// /etc/crowdsec/appsec-configs/jabali-appsec.yaml. Replaces install.sh's
// bash heredoc + reconcile-awk + idempotent-ensure (plans/appsec-config-
// single-source.md). The agent geoblock handler already calls
// appseccfg.Render directly via Go; this puts install.sh on the same
// rail so the schema + the ADR-0102 allowlist live in ONE place.
//
// --reconcile (default true) preserves the operator's
// jabali-mode/jabali-countries header from the existing file. Without
// it (`--reconcile=false`) the file is written at default state
// (mode=off, no countries) — used only on a forced reset.
func newAppSecRenderConfigCmd() *cobra.Command {
	var reconcile bool
	var reload bool
	cmd := &cobra.Command{
		Use:   "render-config",
		Short: "Write /etc/crowdsec/appsec-configs/jabali-appsec.yaml from internal/appseccfg.Render",
		Long: `Idempotent canonical writer for the jabali-appsec.yaml config.
Single source of truth — replaces the install.sh bash heredoc and the
hand-duplicated on_match allowlist. The agent's geoblock handler
already calls internal/appseccfg.Render via Go; this subcommand puts
install.sh + update.go on the same code path.

With --reconcile (default) the operator header (jabali-mode +
jabali-countries) is parsed from the existing file and preserved.
Without it the file is reset to defaults (mode=off, no countries).

Inband rules are presence-gated by stat()ing /etc/crowdsec/appsec-rules/
— a hub regression that removes crs.yaml automatically degrades the
config to vpatch+generic without referencing a missing rule (crowdsec
otherwise refuses to start).

Exits 0 with "written" / "unchanged" on the last line so callers can
gate a 'systemctl reload crowdsec' on real diffs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Targeted CRS false-positive exclusions (jabali CRS "before"
			// plugin). Independent of the geoblock config; loaded before
			// REQUEST-933/949. Write-on-diff; --reload (below) or the caller
			// reloads crowdsec.
			crsChanged, err := writeCRSPluginBefore(cmd)
			if err != nil {
				return err
			}

			mode := "off"
			botMode := "off"
			botScope := "all"
			var countries []string
			if reconcile {
				m, c, bm, bs := readOperatorHeader(appsecConfigPath)
				if m != "" {
					mode = m
				}
				countries = c
				if bm != "" {
					botMode = bm
				}
				if bs != "" {
					botScope = bs
				}
			}
			inband := detectInbandRules(appsecRulesDir)

			// Webmail allowlist sourced from the panel-api
			// reconciler's state file (one FQDN per line, written on
			// every pass). Missing file → empty list → no on_match
			// webmail filter; safe default until the reconciler
			// has run at least once.
			webmailHosts, err := appseccfg.LoadWebmailHosts(appseccfg.WebmailHostsPath)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: load %s: %v\n", appseccfg.WebmailHostsPath, err)
			}

			body := appseccfg.Render(appseccfg.Opts{
				Mode:           mode,
				Countries:      countries,
				Inband:         inband,
				AdminAllowlist: true,
				WebmailHosts:   webmailHosts,
				// Preserve the operator's bot-detection selection across every
				// re-render (install / `jabali update`). Without this the header
				// resets to off and the agent's readback disagrees with the
				// applied acquis. The acquis file itself is owned by install.sh
				// (singular, OFF state) and the agent (plural, ON state); this
				// command only keeps the jabali-appsec header honest.
				BotDetection: botMode,
				// Preserve scope too — a reset from "selected" to "all" would
				// challenge every hosted site at once.
				BotScope: botScope,
			})

			// Write-on-diff: cheap before any nginx/crowdsec reload
			// upstream that may key off mtime or content hash.
			existing, _ := os.ReadFile(appsecConfigPath)
			cfgChanged := string(existing) != body
			if cfgChanged {
				if err := atomicWriteAppSec(appsecConfigPath, body); err != nil {
					return fmt.Errorf("write %s: %w", appsecConfigPath, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"written %s (mode=%s, countries=[%s], inband=%d rules)\n",
					appsecConfigPath, mode, strings.Join(countries, ","), len(inband))
			} else {
				// "unchanged" on the last line is the install.sh reload gate: it
				// reloads crowdsec whenever render-config prints anything else,
				// and a before-plugin write above already made stdout non-empty.
				fmt.Fprintln(cmd.OutOrStdout(), "unchanged")
			}

			// --reload applies the change on this box in one step, best-effort.
			// Off by default: install.sh calls render-config early in an update
			// and owns a single deferred reload at the end of its crowdsec setup,
			// so an update never reloads crowdsec mid-run (the GH discussion #109
			// fresh-install ordering scar). The operator/UI path passes --reload
			// so `exclusion add` + render-config takes effect without a separate
			// `systemctl reload crowdsec` (GH #1653).
			if reload && (crsChanged || cfgChanged) {
				reloadCrowdsec(cmd)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reconcile, "reconcile", true,
		"preserve operator jabali-mode/jabali-countries header from existing file (default true)")
	cmd.Flags().BoolVar(&reload, "reload", false,
		"best-effort `systemctl reload crowdsec` after a real diff (default false; "+
			"install.sh owns its own deferred reload, so leave off inside an update)")
	return cmd
}

// readOperatorHeader parses the two `# jabali-mode:` / `# jabali-countries:`
// lines from the existing file. Both writers (this subcommand + the agent
// geoblock handler) emit those lines so the operator state survives every
// re-render. Missing file → ("", nil) which the caller maps to defaults.
func readOperatorHeader(path string) (mode string, countries []string, botMode, botScope string) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Stop at the first non-comment, non-blank line — header is
		// always at the very top of the rendered file.
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		switch {
		case strings.HasPrefix(line, appsecModeHeader):
			mode = strings.TrimSpace(strings.TrimPrefix(line, appsecModeHeader))
		case strings.HasPrefix(line, appsecCountriesHead):
			csv := strings.TrimSpace(strings.TrimPrefix(line, appsecCountriesHead))
			for _, c := range strings.Split(csv, ",") {
				if c = strings.TrimSpace(c); c != "" {
					countries = append(countries, c)
				}
			}
		case strings.HasPrefix(line, appsecBotScopeHead):
			botScope = strings.TrimSpace(strings.TrimPrefix(line, appsecBotScopeHead))
		case strings.HasPrefix(line, appsecBotDetectHead):
			botMode = strings.TrimSpace(strings.TrimPrefix(line, appsecBotDetectHead))
		}
	}
	return mode, countries, botMode, botScope
}

// detectInbandRules stats the appsec-rules dir and returns the list of
// inband rules that exist on disk, in stable order. vpatch-* and
// generic-* are always included (the pre-flight hub installs guarantee
// them). base-config / crs / crs-exclusion-plugin-wordpress are
// presence-gated — a hub regression that removes one of them automatically
// drops it from the config so crowdsec never references a missing rule.
func detectInbandRules(dir string) []string {
	out := []string{
		"crowdsecurity/vpatch-*",
		"crowdsecurity/generic-*",
	}
	for _, gated := range []struct{ file, rule string }{
		{"base-config.yaml", "crowdsecurity/base-config"},
		{"crs.yaml", "crowdsecurity/crs"},
		{"crs-exclusion-plugin-wordpress.yaml", "crowdsecurity/crs-exclusion-plugin-wordpress"},
	} {
		if _, err := os.Stat(filepath.Join(dir, gated.file)); err == nil {
			out = append(out, gated.rule)
		}
	}
	return out
}

// writeCRSPluginBefore reconciles the two jabali CRS "before" plugin files and
// reports whether either changed (so --reload can gate a crowdsec reload).
// Skips cleanly on a host without crowdsec (CI, dev) so we never create a stray
// /var/lib/crowdsec tree.
//
// The two files, both matched by the crs-plugins/*/*-before.conf glob:
//
//   - CRSPluginBeforePath (jabali-before.conf): the built-in exclusions from
//     appseccfg.CRSPluginBefore(). Written VERBATIM — byte-identical to what the
//     agent's ApplyAppSecBeforePlugin writes at boot — so the two writers never
//     diverge and never clobber each other (GH #1655).
//   - CRSPluginOperatorBeforePath (jabali-operator-before.conf): the
//     operator-managed exclusions from the DB. A SEPARATE file precisely because
//     the agent — which has no DB access — re-renders the built-in file on every
//     boot; before the split it wrote built-ins-only over the combined file and
//     dropped every operator exclusion on each restart (GH #1655).
//
// render-config deliberately has no requireDB PreRunE: install.sh calls it
// early, before the database necessarily exists, and it must still write the
// built-in file on such a host. So the DB is opened best-effort here, and when
// it is unreachable the operator file is left UNTOUCHED (never removed) — a
// transient DB outage must not drop live exclusions and re-ban users, the exact
// failure this split fixes.
func writeCRSPluginBefore(cmd *cobra.Command) (changed bool, err error) {
	if _, statErr := os.Stat("/var/lib/crowdsec/data"); statErr != nil {
		return false, nil // crowdsec not installed here — nothing to manage
	}

	// Fetch operator exclusions best-effort. render-config has no requireDB
	// PreRunE (install.sh calls it before the DB necessarily exists), so a
	// missing or erroring DB must NOT block the built-in write below — and must
	// leave the operator file UNTOUCHED (operatorKnown=false), never removed, so
	// a transient outage cannot drop live exclusions and re-ban users.
	var list []appseccfg.Exclusion
	operatorKnown := false
	if sharedDB == nil {
		if initErr := initConfig(); initErr == nil {
			_ = initDB()
		}
	}
	switch {
	case sharedDB == nil:
		fmt.Fprintf(cmd.OutOrStdout(),
			"! operator CRS exclusions NOT reconciled (database unavailable) — %s left as-is\n",
			appseccfg.CRSPluginOperatorBeforePath)
	default:
		excl, listErr := repository.NewCRSRuleExclusionRepository(sharedDB).List(cmd.Context())
		if listErr != nil {
			fmt.Fprintf(cmd.OutOrStdout(),
				"! operator CRS exclusions NOT reconciled (%v) — %s left as-is\n",
				listErr, appseccfg.CRSPluginOperatorBeforePath)
		} else {
			list = make([]appseccfg.Exclusion, 0, len(excl))
			for _, e := range excl {
				list = append(list, appseccfg.Exclusion{
					Host: e.Host, URIPrefix: e.URIPrefix, RuleID: e.RuleID, Note: e.Note,
				})
			}
			operatorKnown = true
		}
	}

	return reconcileCRSBeforeFiles(cmd.OutOrStdout(),
		appseccfg.CRSPluginBeforePath, appseccfg.CRSPluginOperatorBeforePath, list, operatorKnown)
}

// reconcileCRSBeforeFiles writes the built-in before-plugin file verbatim and,
// when the operator exclusions are known (DB reachable), the operator file —
// removing it when the list is empty so a since-removed exclusion cannot linger
// live. When operatorKnown is false the operator file is left exactly as-is.
// Returns whether anything on disk changed (so --reload can gate a reload).
//
// Split from writeCRSPluginBefore so the invariant this fix (GH #1655) rests on
// is unit-testable without a DB or the real /var/lib/crowdsec paths: the
// built-in file is written byte-identical to what the agent boot writer emits
// (appseccfg.CRSPluginBefore, no operator content appended), and operator
// content only ever lands in operatorPath.
func reconcileCRSBeforeFiles(out io.Writer, builtinPath, operatorPath string,
	list []appseccfg.Exclusion, operatorKnown bool) (changed bool, err error) {

	// 1. Built-in exclusions → builtinPath, VERBATIM.
	wrote, err := writeOnDiff(builtinPath, appseccfg.CRSPluginBefore())
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = true
		fmt.Fprintf(out, "written %s (CRS before-plugin, built-in)\n", builtinPath)
	}

	if !operatorKnown {
		return changed, nil // DB unreachable — leave the operator file untouched
	}

	// 2. Operator-managed exclusions → operatorPath.
	body := appseccfg.RenderOperatorBeforeFile(list)
	if body == "" {
		// No operator exclusions: remove the file so a since-removed exclusion
		// cannot linger live. Absent already → no-op.
		removed, rmErr := removeIfPresent(operatorPath)
		if rmErr != nil {
			return changed, fmt.Errorf("remove %s: %w", operatorPath, rmErr)
		}
		if removed {
			changed = true
			fmt.Fprintf(out, "removed %s (no operator exclusions)\n", operatorPath)
		}
		return changed, nil
	}
	wrote, err = writeOnDiff(operatorPath, body)
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = true
		fmt.Fprintf(out, "written %s (CRS before-plugin, %d operator exclusion(s))\n", operatorPath, len(list))
	}
	return changed, nil
}

// writeOnDiff writes body to path via atomicWriteAppSec only when the current
// content differs. Returns whether it wrote.
func writeOnDiff(path, body string) (bool, error) {
	existing, _ := os.ReadFile(path)
	if string(existing) == body {
		return false, nil
	}
	if err := atomicWriteAppSec(path, body); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// removeIfPresent removes path, treating an already-absent file as success.
// Returns whether it actually removed anything.
func removeIfPresent(path string) (bool, error) {
	err := os.Remove(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// reloadCrowdsec applies a freshly-written appsec/before-plugin change on the
// box. Best-effort, mirroring the agent boot path (panel-agent
// security_appsec_before.go): reload, fall back to restart, never fail the
// command. Gated on the CRS data tree so CI/dev never shells out. Invoked only
// under --reload; install.sh leaves the flag off and owns its own deferred
// reload (GH #1653 / the GH discussion #109 fresh-install ordering scar).
func reloadCrowdsec(cmd *cobra.Command) {
	if _, err := os.Stat("/var/lib/crowdsec/data"); err != nil {
		return // crowdsec not installed — nothing to reload
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		if out2, err2 := exec.CommandContext(ctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err2 != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warn: crowdsec reload+restart failed: reload=%v (%s) restart=%v (%s)\n",
				err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
			return
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "reloaded crowdsec")
}

// atomicWriteAppSec writes via tmpfile + rename in the same dir so the
// reader never sees a half-written file. 0644 root:root matches the
// installed perms install.sh seeded.
func atomicWriteAppSec(path, body string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "jabali-appsec-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once rename succeeded
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
