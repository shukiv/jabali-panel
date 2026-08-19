package main

import (
	"os"
	"strings"
	"testing"
)

// TestDomainAdvancedSSLMode_PersistsThroughDedicatedWriter guards the JAB-313
// fix. ssl_mode is authoritative (ADR-0141) but is NOT in the general
// domainRepo.Update column allowlist, so `domain advanced --ssl-mode=…` set the
// field on the in-memory struct and then called Update, which silently dropped
// it — the command reported success while the mode never changed. The fix
// persists it through the dedicated UpdateSSLMode writer (as the HTTP path and
// ssl_custom_cmd.go do). cmd/server's advanced command runs on the global repo
// (no injection seam), so this source-pins the dedicated call, matching the
// repo's precedent (log_cmd_scope_test.go).
func TestDomainAdvancedSSLMode_PersistsThroughDedicatedWriter(t *testing.T) {
	src, err := os.ReadFile("domain_advanced_cmd.go")
	if err != nil {
		t.Fatalf("read domain_advanced_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "UpdateSSLMode(ctx, d.ID, sslMode)") {
		t.Fatal("--ssl-mode must persist through domainRepo.UpdateSSLMode — the general Update allowlist drops ssl_mode, so relying on it silently no-ops the mode change (JAB-313)")
	}
	if !strings.Contains(s, `cmd.Flags().Changed("ssl-mode")`) {
		t.Fatal("the dedicated UpdateSSLMode persistence must be gated on --ssl-mode actually changing (JAB-313)")
	}
	// Sibling silent-drop: cache_enabled is also excluded from the Update
	// allowlist and has its own writer, so --cache must go through it too.
	if !strings.Contains(s, "UpdateCacheEnabled(ctx, d.ID, d.CacheEnabled)") {
		t.Fatal("--cache must persist through domainRepo.UpdateCacheEnabled — cache_enabled is not in the Update allowlist either (JAB-313)")
	}
}
