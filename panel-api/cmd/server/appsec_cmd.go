package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			// REQUEST-933/949. Write-on-diff; the caller reloads crowdsec.
			if err := writeCRSPluginBefore(cmd); err != nil {
				return err
			}

			mode := "off"
			var countries []string
			if reconcile {
				m, c := readOperatorHeader(appsecConfigPath)
				if m != "" {
					mode = m
				}
				countries = c
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
			})

			// Write-on-diff: cheap before any nginx/crowdsec reload
			// upstream that may key off mtime or content hash.
			existing, _ := os.ReadFile(appsecConfigPath)
			if string(existing) == body {
				fmt.Fprintln(cmd.OutOrStdout(), "unchanged")
				return nil
			}
			if err := atomicWriteAppSec(appsecConfigPath, body); err != nil {
				return fmt.Errorf("write %s: %w", appsecConfigPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"written %s (mode=%s, countries=[%s], inband=%d rules)\n",
				appsecConfigPath, mode, strings.Join(countries, ","), len(inband))
			return nil
		},
	}
	cmd.Flags().BoolVar(&reconcile, "reconcile", true,
		"preserve operator jabali-mode/jabali-countries header from existing file (default true)")
	return cmd
}

// readOperatorHeader parses the two `# jabali-mode:` / `# jabali-countries:`
// lines from the existing file. Both writers (this subcommand + the agent
// geoblock handler) emit those lines so the operator state survives every
// re-render. Missing file → ("", nil) which the caller maps to defaults.
func readOperatorHeader(path string) (mode string, countries []string) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
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
		}
	}
	return mode, countries
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

// writeCRSPluginBefore writes the jabali CRS "before" plugin
// (appseccfg.CRSPluginBefore) to its CRS data path. Skips cleanly on a
// host without crowdsec installed (CI, dev) so we never create a stray
// /var/lib/crowdsec tree. Write-on-diff: returns without writing when
// the content already matches.
func writeCRSPluginBefore(cmd *cobra.Command) error {
	if _, err := os.Stat("/var/lib/crowdsec/data"); err != nil {
		return nil // crowdsec not installed here — nothing to manage
	}
	body := appseccfg.CRSPluginBefore()

	// JAB-227: append operator-managed exclusions. They live in the DB rather
	// than in this static template because only the operator can see the false
	// positives specific to their tenants' apps. Best-effort: a DB that is not
	// reachable must not stop the built-in exclusions being written, but the
	// operator has to be told their entries are missing from this render rather
	// than left assuming they applied.
	// render-config deliberately has no requireDB PreRunE: install.sh calls it
	// early, before the database necessarily exists, and it must still write the
	// built-in exclusions on such a host. So open the DB best-effort here and
	// carry on without the operator section if it is not reachable.
	if sharedDB == nil {
		if err := initConfig(); err == nil {
			_ = initDB()
		}
	}
	if sharedDB != nil {
		excl, err := repository.NewCRSRuleExclusionRepository(sharedDB).List(cmd.Context())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(),
				"! operator CRS exclusions NOT rendered (%v) — built-in exclusions written without them\n", err)
		} else if len(excl) > 0 {
			list := make([]appseccfg.Exclusion, 0, len(excl))
			for _, e := range excl {
				list = append(list, appseccfg.Exclusion{
					Host: e.Host, URIPrefix: e.URIPrefix, RuleID: e.RuleID, Note: e.Note,
				})
			}
			body += appseccfg.RenderExclusions(list)
		}
	}

	existing, _ := os.ReadFile(appseccfg.CRSPluginBeforePath)
	if string(existing) == body {
		return nil
	}
	if err := atomicWriteAppSec(appseccfg.CRSPluginBeforePath, body); err != nil {
		return fmt.Errorf("write %s: %w", appseccfg.CRSPluginBeforePath, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "written %s (CRS before-plugin)\n", appseccfg.CRSPluginBeforePath)
	return nil
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
