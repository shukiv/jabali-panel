package reconciler

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestRenderActiveRules_OffModeIsValidEmpty guards GH #718.
//
// mode=off must NOT emit `sp.global.enable(0);`. That directive is not valid in
// the shipped Snuffleupagus (v0.13 — no master switch); it fatals PHP-FPM with
// "Unexpected keyword 'enable' ... Invalid configuration file ... Could not
// startup" and 500s every WordPress/PHP site on the pool. "Off" is a
// comment-only (empty) ruleset carrying the `# mode=off` marker the agent's
// status handler reads.
func TestRenderActiveRules_OffModeIsValidEmpty(t *testing.T) {
	out, err := renderActiveRules(models.SnuffleupagusModeOff, nil)
	if err != nil {
		t.Fatalf("renderActiveRules(off): %v", err)
	}
	s := string(out)

	if strings.Contains(s, "sp.global.enable") {
		t.Errorf("off render still emits the invalid sp.global.enable directive:\n%s", s)
	}
	if !strings.Contains(s, "# mode=off") {
		t.Errorf("off render missing the `# mode=off` marker the agent reads:\n%s", s)
	}

	// Every non-blank line must be a comment — an "off" ruleset carries no
	// active directive at all (empty ruleset = enforce nothing).
	for _, ln := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		t.Errorf("off render has a non-comment directive line %q — off must be empty", trimmed)
	}

	// ASCII-only: any byte > 0x7f (em-dash, smart quote) corrupts the
	// snuffleupagus lexer and silently swallows subsequent rules.
	for i := 0; i < len(out); i++ {
		if out[i] > 0x7f {
			t.Errorf("off render has non-ASCII byte 0x%x at offset %d — lexer corruption risk", out[i], i)
			break
		}
	}
}
