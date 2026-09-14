package main

import (
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestSSLEnableTargetMode_PreservesOperatorLineage guards the JAB-356 clobber
// constraint. The legacy `ssl enable` door is "enable ACME" (GH #246), so it
// switches to Let's Encrypt for the ACME-managed and untrusted-bootstrap modes
// — but it must PRESERVE an operator-provided certificate lineage (`custom`
// uploaded pair, `shared` JAB-170 cert). Forcing `le` there would kick off a
// fresh ACME issuance over the operator's cert (the 2026-05-09 LE-clobber
// class; see internal/reconciler/ssl_san_drift.go).
func TestSSLEnableTargetMode_PreservesOperatorLineage(t *testing.T) {
	cases := []struct {
		current string
		want    string
	}{
		{models.SSLModeNone, models.SSLModeLE},       // enabling from off -> ACME
		{"", models.SSLModeLE},                       // unset -> ACME
		{models.SSLModeSelf, models.SSLModeLE},       // self-signed bootstrap is transient -> ACME
		{models.SSLModeLE, models.SSLModeLE},         // already ACME -> ACME (idempotent mode)
		{models.SSLModeCustom, models.SSLModeCustom}, // uploaded cert lineage preserved
		{models.SSLModeShared, models.SSLModeShared}, // shared cert lineage preserved
	}
	for _, tc := range cases {
		if got := sslEnableTargetMode(tc.current); got != tc.want {
			t.Errorf("sslEnableTargetMode(%q) = %q, want %q", tc.current, got, tc.want)
		}
	}
}

// TestSSLEnableDisable_PersistThroughUpdateSSLMode source-pins the JAB-356 fix.
// cmd/server's ssl enable/disable run on the global repo (no injection seam),
// so — matching the sibling JAB-313 pin (domain_advanced_cmd_ssl_mode_test.go)
// — this asserts both doors persist the authoritative mode through the
// dedicated UpdateSSLMode writer rather than the general Update, whose column
// allowlist silently drops ssl_mode. HTTP enableSSL/disableSSL (ssl.go) already
// do this; the CLI was the drifted door.
func TestSSLEnableDisable_PersistThroughUpdateSSLMode(t *testing.T) {
	src, err := os.ReadFile("ssl_cmd.go")
	if err != nil {
		t.Fatalf("read ssl_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "mode := sslEnableTargetMode(dom.SSLMode)") ||
		!strings.Contains(s, "UpdateSSLMode(ctx, dom.ID, mode)") {
		t.Fatal("`ssl enable` must persist the authoritative mode through domainRepo.UpdateSSLMode via sslEnableTargetMode — the general Update allowlist drops ssl_mode, leaving ssl_enabled=true / ssl_mode=none (JAB-356)")
	}
	if !strings.Contains(s, "UpdateSSLMode(ctx, dom.ID, models.SSLModeNone)") {
		t.Fatal("`ssl disable` must persist ssl_mode=none through domainRepo.UpdateSSLMode — the general Update allowlist drops ssl_mode, leaving the mode stale (JAB-356)")
	}
}
