package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden parity at the regenerator boundary: csAppSecGeoblockSetHandler
// fully rewrites jabali-appsec.yaml on every Apply, so this output is
// the file the host actually runs. Behavior must be the old template
// PLUS the ADR-0102 admin allowlist (= what PR #12 did, now
// single-sourced via internal/appseccfg).
func TestRenderAppSecGeoblockRule_Golden(t *testing.T) {
	appsecGoldenEnv(t, "base-config.yaml", "crs.yaml", "crs-exclusion-plugin-wordpress.yaml")
	off := renderAppSecGeoblockRule("off", nil)
	host, _ := os.Hostname() // GH #706: admin allowlist is scoped to the panel host
	for _, want := range []string{
		"# jabali-mode: off\n",
		"name: crowdsecurity/jabali-appsec\ndefault_remediation: ban\n",
		// The rules on disk, in render-config's order (appseccfg.DetectInbandRules).
		"inband_rules:\n - crowdsecurity/vpatch-*\n - crowdsecurity/generic-*\n - crowdsecurity/base-config\n - crowdsecurity/crs\n - crowdsecurity/crs-exclusion-plugin-wordpress\non_match:",
		"on_match:\n - filter: (req.URL.Path startsWith \"/api/v1/\" || req.URL.Path startsWith \"/phpmyadmin/\" || req.URL.Path startsWith \"/jabali-adminer/\" || req.URL.Path startsWith \"/dav/\") && req.Host == \"" + host + "\"\n   apply:\n    - CancelEvent()\n    - CancelAlert()\n    - SetRemediation(\"allow\")\n",
	} {
		if !strings.Contains(off, want) {
			t.Fatalf("off mode missing %q\n%s", want, off)
		}
	}
	if strings.Contains(off, "pre_eval:") {
		t.Fatalf("off must not emit pre_eval:\n%s", off)
	}
	if strings.Index(off, "inband_rules:") > strings.Index(off, "on_match:") {
		t.Fatal("inband_rules must precede on_match")
	}

	allow := renderAppSecGeoblockRule("allow", []string{"IL"})
	if !strings.Contains(allow, `not in ["IL", ""]`) ||
		!strings.Contains(allow, `DropRequest("Forbidden Country (jabali allow-list)")`) {
		t.Fatalf("allow pre_eval wrong:\n%s", allow)
	}
	if !strings.Contains(allow, "on_match:") || strings.Index(allow, "on_match:") > strings.Index(allow, "pre_eval:") {
		t.Fatal("on_match must be present and precede pre_eval (allow)")
	}
	deny := renderAppSecGeoblockRule("deny", []string{"RU"})
	if !strings.Contains(deny, `in ["RU"]`) ||
		!strings.Contains(deny, `DropRequest("Forbidden Country (jabali deny-list)")`) {
		t.Fatalf("deny pre_eval wrong:\n%s", deny)
	}
}

// appsecGoldenEnv points the renderer at a temp rules dir holding files, and at
// an absent jabali-appsec.yaml, so the host the test runs on cannot leak in.
func appsecGoldenEnv(t *testing.T, files ...string) {
	t.Helper()
	dir := t.TempDir()
	prevRules, prevRule := appsecRulesDir, appsecRulePath
	appsecRulesDir = filepath.Join(dir, "appsec-rules")
	appsecRulePath = filepath.Join(dir, "jabali-appsec.yaml")
	t.Cleanup(func() { appsecRulesDir, appsecRulePath = prevRules, prevRule })
	if err := os.MkdirAll(appsecRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(appsecRulesDir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A geoblock change re-renders the whole file. It must keep every rule set
// render-config loaded: the in-band list comes from the rules on disk, not a
// fixed list of the agent's own.
func TestRenderAppSecGeoblockRule_KeepsInbandRulesOnDisk(t *testing.T) {
	appsecGoldenEnv(t, "crs.yaml")
	out := renderAppSecGeoblockRule("deny", []string{"RU"})
	if !strings.Contains(out, " - crowdsecurity/crs\n") {
		t.Fatalf("crowdsecurity/crs is on disk but not loaded:\n%s", out)
	}
	if strings.Contains(out, "crowdsecurity/base-config") {
		t.Fatalf("base-config is not on disk, so referencing it would stop crowdsec:\n%s", out)
	}
}
