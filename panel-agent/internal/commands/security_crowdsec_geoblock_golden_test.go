package commands

import (
	"os"
	"strings"
	"testing"
)

// Golden parity at the regenerator boundary: csAppSecGeoblockSetHandler
// fully rewrites jabali-appsec.yaml on every Apply, so this output is
// the file the host actually runs. Behavior must be the old template
// PLUS the ADR-0102 admin allowlist (= what PR #12 did, now
// single-sourced via internal/appseccfg).
func TestRenderAppSecGeoblockRule_Golden(t *testing.T) {
	off := renderAppSecGeoblockRule("off", nil)
	host, _ := os.Hostname() // GH #706: admin allowlist is scoped to the panel host
	for _, want := range []string{
		"# jabali-mode: off\n",
		"name: crowdsecurity/jabali-appsec\ndefault_remediation: ban\n",
		"inband_rules:\n - crowdsecurity/base-config\n - crowdsecurity/vpatch-*\n - crowdsecurity/generic-*\n",
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
