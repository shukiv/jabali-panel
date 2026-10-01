package reconciler

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// repoSnufBundle is the rules bundle in this repo, relative to this package.
const repoSnufBundle = "../../../install/snuffleupagus/rules"

// firstNonASCII returns the 1-based line of the first byte > 0x7f, or 0.
func firstNonASCII(data []byte) (line int, b byte) {
	for i, c := range data {
		if c > 0x7f {
			return bytes.Count(data[:i], []byte("\n")) + 1, c
		}
	}
	return 0, 0
}

// Snuffleupagus up to v0.13 stops parsing at the first byte > 0x7f, even one
// in a comment, and silently drops every rule after it. Em-dashes in
// 00-base.rules comments left only the first few rules loaded from
// 2026-07-02 on. v0.14 refuses a non-ASCII byte inside a rule, and PHP does
// not start.
func TestSnuffleupagusBundle_IsASCII(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoSnufBundle, "*.rules"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no rules files under %s (err=%v)", repoSnufBundle, err)
	}
	// pending/ is not loaded yet, but its rules are meant to come back.
	pending, err := filepath.Glob(filepath.Join(repoSnufBundle, "pending", "*.rules"))
	if err != nil || len(pending) == 0 {
		t.Fatalf("no rules files under %s/pending (err=%v)", repoSnufBundle, err)
	}
	files = append(files, pending...)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if line, b := firstNonASCII(data); line > 0 {
			t.Errorf("%s:%d: non-ASCII byte 0x%02x; Snuffleupagus v0.13 drops every rule after it", filepath.Base(f), line, b)
		}
	}
}

// The rendered file must stay ASCII in every mode that loads rules, also when
// an operator writes a non-ASCII reason for a disabled rule.
func TestRenderActiveRules_IsASCII(t *testing.T) {
	prev := snufBundleDir
	snufBundleDir = repoSnufBundle
	t.Cleanup(func() { snufBundleDir = prev })

	reason := "breaks the shop \u2014 see ticket"
	overrides := []models.SnuffleupagusRuleOverride{{
		RuleName: "00-base.rules#1 sp.log_media",
		Enabled:  false,
		Reason:   &reason,
		SetAt:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}}
	for _, mode := range []models.SnuffleupagusMode{models.SnuffleupagusModeSimulation, models.SnuffleupagusModeEnforce} {
		out, err := renderActiveRules(mode, overrides)
		if err != nil {
			t.Fatalf("renderActiveRules(%s): %v", mode, err)
		}
		s := string(out)
		if !strings.Contains(s, "# --- 00-base.rules ---") || !strings.Contains(s, `sp.disable_function.function("phpinfo")`) {
			t.Fatalf("%s render did not load the repo bundle:\n%.400s", mode, s)
		}
		if want := "reason=" + strconv.QuoteToASCII(reason); !strings.Contains(s, want) {
			t.Errorf("%s render does not carry %s", mode, want)
		}
		if line, b := firstNonASCII(out); line > 0 {
			t.Errorf("%s render line %d: non-ASCII byte 0x%02x", mode, line, b)
		}
	}
}

// The rules in pending/ were never loaded from 2026-07-02 on, and loaded as
// written they break WordPress, Drupal, phpBB and Laravel apps. Until each
// passes a soak, the render must hold exactly the rules boxes enforced before.
func TestRenderActiveRules_PendingRulesStayOut(t *testing.T) {
	prev := snufBundleDir
	snufBundleDir = repoSnufBundle
	t.Cleanup(func() { snufBundleDir = prev })

	out, err := renderActiveRules(models.SnuffleupagusModeEnforce, nil)
	if err != nil {
		t.Fatalf("renderActiveRules(enforce): %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`sp.disable_function.function("system").drop();`,
		`sp.disable_function.function("proc_open").drop();`,
		`sp.disable_function.function("phpinfo").drop();`,
		`sp.unserialize_hmac.enable();`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("render lacks loaded rule %s", want)
		}
	}
	for _, fn := range []string{"putenv", "file_put_contents", "require", "include", "ini_set", "call_user_func", "move_uploaded_file", "curl_setopt"} {
		if strings.Contains(s, `sp.disable_function.function("`+fn+`")`) {
			t.Errorf("render loads a pending %s rule", fn)
		}
	}
	if strings.Contains(s, "sp.eval_blacklist") || strings.Contains(s, "10-wordpress.rules") {
		t.Error("render loads pending rules (eval_blacklist or a CMS overlay)")
	}
}
