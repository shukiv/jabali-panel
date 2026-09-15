package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestSSLEnableIsOperatorLineage guards the JAB-356 clobber constraint. The
// legacy `ssl enable` door is "enable ACME" (GH #246), so it switches to Let's
// Encrypt for all other modes (none / empty / self / le) — but it must treat an
// operator-provided certificate lineage (`custom` uploaded pair, `shared`
// JAB-170 cert) as a no-op, never clobbering it with a fresh ACME issuance (the
// 2026-05-09 LE-clobber class; see internal/reconciler/ssl_san_drift.go).
func TestSSLEnableIsOperatorLineage(t *testing.T) {
	cases := []struct {
		mode string
		want bool
	}{
		{models.SSLModeCustom, true},  // uploaded cert lineage — preserve
		{models.SSLModeShared, true},  // shared cert lineage — preserve
		{models.SSLModeNone, false},   // enabling from off -> ACME
		{"", false},                   // unset -> ACME
		{models.SSLModeSelf, false},   // panel-generated self-signed, regenerable -> ACME
		{models.SSLModeLE, false},     // already ACME -> ACME
	}
	for _, tc := range cases {
		if got := sslEnableIsOperatorLineage(tc.mode); got != tc.want {
			t.Errorf("sslEnableIsOperatorLineage(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// TestSSLDisableRefusal_ProtectedDomains guards the JAB-356 AC2 invariant on the
// CLI disable door. Now that `ssl disable` writes the authoritative ssl_mode=none
// (so the reconciler revokes for real), it must refuse to strip TLS from the
// panel hostname or a mail-enabled domain, mirroring the set-mode HTTP door
// (internal/api/domains.go). Otherwise the CLI opens the #1507 lockout class.
func TestSSLDisableRefusal_ProtectedDomains(t *testing.T) {
	if err := sslDisableRefusal(&models.Domain{Name: "panel.example.com", IsPanelPrimary: true}); err == nil {
		t.Error("disable must refuse to drop TLS on the panel-primary domain")
	}
	if err := sslDisableRefusal(&models.Domain{Name: "mail.example.com", EmailEnabled: true}); err == nil {
		t.Error("disable must refuse to drop TLS on a mail-enabled domain")
	}
	if err := sslDisableRefusal(&models.Domain{Name: "plain.example.com"}); err != nil {
		t.Errorf("disable must be allowed on an ordinary domain, got %v", err)
	}
}

// TestSSLEnableDisable_PersistThroughUpdateSSLMode source-pins the JAB-356 fix.
// cmd/server's ssl enable/disable run on the global repo (no injection seam),
// so — matching the sibling JAB-313 pin (domain_advanced_cmd_ssl_mode_test.go)
// — this asserts both doors persist the authoritative mode through the
// dedicated UpdateSSLMode writer rather than the general Update, whose column
// allowlist silently drops ssl_mode, and that each guard is wired in.
func TestSSLEnableDisable_PersistThroughUpdateSSLMode(t *testing.T) {
	src, err := os.ReadFile("ssl_cmd.go")
	if err != nil {
		t.Fatalf("read ssl_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "sslEnableIsOperatorLineage(dom.SSLMode)") ||
		!strings.Contains(s, "UpdateSSLMode(ctx, dom.ID, models.SSLModeLE)") {
		t.Fatal("`ssl enable` must skip an operator lineage (sslEnableIsOperatorLineage) then persist ssl_mode=le through domainRepo.UpdateSSLMode — the general Update allowlist drops ssl_mode (JAB-356)")
	}
	if !strings.Contains(s, "sslDisableRefusal(dom)") ||
		!strings.Contains(s, "UpdateSSLMode(ctx, dom.ID, models.SSLModeNone)") {
		t.Fatal("`ssl disable` must enforce the protected-domain refusal (sslDisableRefusal) then persist ssl_mode=none through domainRepo.UpdateSSLMode — the general Update allowlist drops ssl_mode, so the reconciler never dropped TLS (JAB-356)")
	}
}

// TestSSLEnableAlreadyIssued guards JAB-356 AC3: enabling a domain that is
// already on Let's Encrypt with a valid issued certificate is idempotent, so
// the CLI must NOT reset the cert row to pending (which re-runs certbot and
// burns LE's duplicate-certificate rate limit). The load-bearing case is
// (none, issued, >30d) -> false: `ssl disable` writes ssl_mode=none but leaves
// the cert row `issued` until the reconciler tick revokes it, so a guard keyed
// on cert status alone would make the next `ssl enable` a wrong-direction no-op.
func TestSSLEnableAlreadyIssued(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	future := now.Add(60 * 24 * time.Hour) // 60d — comfortably valid
	soon := now.Add(10 * 24 * time.Hour)   // 10d — inside the 30d renewal floor
	issuedFuture := &models.SSLCertificate{Status: models.SSLStatusIssued, ExpiresAt: &future}

	cases := []struct {
		name string
		mode string
		cert *models.SSLCertificate
		want bool
	}{
		{"le + issued + >30d -> idempotent no-op", models.SSLModeLE, issuedFuture, true},
		{"le + issued + <=30d -> proceed (renewal)", models.SSLModeLE, &models.SSLCertificate{Status: models.SSLStatusIssued, ExpiresAt: &soon}, false},
		{"le + issued + nil expiry -> proceed", models.SSLModeLE, &models.SSLCertificate{Status: models.SSLStatusIssued}, false},
		// Load-bearing: post-`ssl disable` the mode is none but the row is still
		// issued until the tick revokes it — enabling must proceed, not no-op.
		{"none + issued + >30d -> proceed (post-disable stale row)", models.SSLModeNone, issuedFuture, false},
		{"self + issued + >30d -> proceed (switch to le)", models.SSLModeSelf, issuedFuture, false},
		{"le + pending -> proceed", models.SSLModeLE, &models.SSLCertificate{Status: models.SSLStatusPending, ExpiresAt: &future}, false},
		{"le + nil cert -> proceed", models.SSLModeLE, nil, false},
	}
	for _, tc := range cases {
		if got := sslEnableAlreadyIssued(tc.mode, tc.cert, now); got != tc.want {
			t.Errorf("%s: sslEnableAlreadyIssued(%q, ...) = %v, want %v", tc.name, tc.mode, got, tc.want)
		}
	}
}

// TestSSLEnable_IdempotentGuardBeforeWrite source-pins the AC3 wiring: cmd/server
// runs on the global repo (no injection seam), so — matching the sibling pins
// above — this asserts the enable door consults sslEnableAlreadyIssued AND that
// the guard runs BEFORE the UpdateSSLMode(le)/pending-reset writes, so a valid
// issued cert is never clobbered back to pending.
func TestSSLEnable_IdempotentGuardBeforeWrite(t *testing.T) {
	src, err := os.ReadFile("ssl_cmd.go")
	if err != nil {
		t.Fatalf("read ssl_cmd.go: %v", err)
	}
	s := string(src)
	guard := "sslEnableAlreadyIssued(dom.SSLMode, cert, time.Now())"
	write := "UpdateSSLMode(ctx, dom.ID, models.SSLModeLE)"
	gi := strings.Index(s, guard)
	wi := strings.Index(s, write)
	if gi < 0 {
		t.Fatal("`ssl enable` must consult sslEnableAlreadyIssued(dom.SSLMode, cert, time.Now()) so an already-issued cert is not reset to pending (JAB-356 AC3)")
	}
	if wi < 0 || gi > wi {
		t.Fatal("the AC3 idempotency guard must run BEFORE UpdateSSLMode(le)/the pending reset — otherwise a valid issued cert is clobbered back to pending (re-runs certbot, burns LE's duplicate-certificate rate limit)")
	}
}
