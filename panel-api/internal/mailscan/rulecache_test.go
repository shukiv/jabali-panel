package mailscan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The fingerprint is what invalidates a compiled blob when maldet updates the
// rule pack. If it failed to change, hosts would keep scanning with stale
// rules — strictly worse than the recompile-every-time behaviour this
// replaces, because it would be silent.
func TestRulesFingerprintChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	rule := filepath.Join(dir, "a.yara")
	if err := os.WriteFile(rule, []byte("rule a { condition: true }"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := rulesFingerprint([]string{rule})

	// Same content, same stat → same fingerprint (no needless recompiles).
	if again := rulesFingerprint([]string{rule}); again != first {
		t.Fatal("fingerprint is not stable for unchanged rules")
	}

	// Content grows (maldet pack update) → must change.
	if err := os.WriteFile(rule, []byte("rule a { condition: true }\nrule b { condition: false }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed := rulesFingerprint([]string{rule}); changed == first {
		t.Fatal("fingerprint unchanged after the rule file changed — a stale " +
			"compiled blob would keep being used")
	}
}

// A missing rule source must still produce a fingerprint (and a different one
// than when it exists), so removing a pack invalidates the cache too.
func TestRulesFingerprintHandlesMissingSource(t *testing.T) {
	dir := t.TempDir()
	rule := filepath.Join(dir, "gone.yara")
	if err := os.WriteFile(rule, []byte("rule a { condition: true }"), 0o644); err != nil {
		t.Fatal(err)
	}
	present := rulesFingerprint([]string{rule})
	if err := os.Remove(rule); err != nil {
		t.Fatal(err)
	}
	if absent := rulesFingerprint([]string{rule}); absent == present {
		t.Fatal("fingerprint unchanged after the rule source disappeared")
	}
}

// The whole optimization is opportunistic: anything unexpected must fall back
// to scanning from source rather than break scanning. With no rule sources, or
// with a yr binary that cannot compile, compiledRules must return "".
func TestCompiledRulesFallsBackWhenUnavailable(t *testing.T) {
	if got := compiledRules(context.Background(), nil); got != "" {
		t.Fatalf("no rule sources: want fallback (empty), got %q", got)
	}

	// Point yr at something that is not yara-x: capability discovery must
	// fail closed to the source-scanning path.
	t.Setenv("JABALI_YR_PATH", "/bin/false")
	ruleCache.mu.Lock()
	ruleCache.unsupported = false
	ruleCache.path = ""
	ruleCache.checkedAt = time.Time{}
	ruleCache.mu.Unlock()

	dir := t.TempDir()
	rule := filepath.Join(dir, "a.yara")
	if err := os.WriteFile(rule, []byte("rule a { condition: true }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := compiledRules(context.Background(), []string{rule}); got != "" {
		t.Fatalf("yr without a usable compile subcommand: want fallback, got %q", got)
	}
}
