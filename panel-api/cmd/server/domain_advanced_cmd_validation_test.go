package main

import (
	"os"
	"strings"
	"testing"
)

// TestValidateDomainSetInput guards the JAB-318 parity fix: the CLI `domain set`
// path must apply the SAME field validation the HTTP PATCH handler enforces
// before the general Update persists the value verbatim. Each case exercises one
// field; falsify by deleting that field's check in validateDomainSetInput and
// the matching row reddens (the unsafe value would then be accepted and written).
func TestValidateDomainSetInput(t *testing.T) {
	sp := func(s string) *string { return &s }

	cases := []struct {
		name        string
		nginx       *string
		redirectTo  *string
		redirectTyp *string
		index       *string
		wantErr     bool
		errSub      string // substring the error must contain (when wantErr)
		wantNormTyp string // expected normalised redirect type (when no error)
	}{
		// nginx directives — the admin allowlist / brace / null-byte checks.
		{name: "nginx disallowed directive", nginx: sp("root /etc/jabali-panel;"), wantErr: true, errSub: "--nginx-directives"},
		{name: "nginx access_log off forbidden", nginx: sp("access_log off;"), wantErr: true, errSub: "--nginx-directives"},
		{name: "nginx unbalanced brace", nginx: sp("location / {"), wantErr: true, errSub: "--nginx-directives"},
		{name: "nginx null byte", nginx: sp("add_header X \x00;"), wantErr: true, errSub: "--nginx-directives"},
		{name: "nginx allowed directive ok", nginx: sp("proxy_set_header X-Test 1;")},
		{name: "nginx empty ok", nginx: sp("")},

		// redirect destination — scheme + host.
		{name: "redirect ftp scheme rejected", redirectTo: sp("ftp://example.com"), wantErr: true, errSub: "--redirect-all-to"},
		{name: "redirect no host rejected", redirectTo: sp("https://"), wantErr: true, errSub: "--redirect-all-to"},
		{name: "redirect https ok", redirectTo: sp("https://example.com/new")},
		{name: "redirect empty clears (ok)", redirectTo: sp("   ")},

		// index priority — enum.
		{name: "index invalid rejected", index: sp("sideways_first"), wantErr: true, errSub: "--index-priority"},
		{name: "index empty rejected (parity)", index: sp(""), wantErr: true, errSub: "--index-priority"},
		{name: "index php_first ok", index: sp("php_first")},

		// redirect type — normalised to the numeric nginx return code.
		{name: "type permanent -> 301", redirectTyp: sp("permanent"), wantNormTyp: "301"},
		{name: "type temporary -> 302", redirectTyp: sp("temporary"), wantNormTyp: "302"},
		{name: "type numeric 308 passes", redirectTyp: sp("308"), wantNormTyp: "308"},
		{name: "type empty clears (ok)", redirectTyp: sp(""), wantNormTyp: ""},
		{name: "type 303 rejected (not an allowed code)", redirectTyp: sp("303"), wantErr: true, errSub: "--redirect-type"},
		{name: "type garbage rejected", redirectTyp: sp("forever"), wantErr: true, errSub: "--redirect-type"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			norm, err := validateDomainSetInput(tc.nginx, tc.redirectTo, tc.redirectTyp, tc.index)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q must contain %q", err.Error(), tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if norm != tc.wantNormTyp {
				t.Fatalf("normalised redirect type = %q, want %q", norm, tc.wantNormTyp)
			}
		})
	}
}

// TestValidateDomainSetInput_OnlyChecksSetFlags: a nil pointer means the flag was
// not set, so an absent field must never be validated (else `domain set --cache`
// alone would wrongly reject the empty index/redirect). All-nil is a no-op.
func TestValidateDomainSetInput_OnlyChecksSetFlags(t *testing.T) {
	norm, err := validateDomainSetInput(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("all-nil input must not error, got %v", err)
	}
	if norm != "" {
		t.Fatalf("no redirect-type set must yield empty norm, got %q", norm)
	}
}

// TestDomainSet_RunEWiresValidation source-pins that the RunE actually calls the
// pure validator before persisting — a behaviour test on the helper alone can't
// prove the command invokes it, and cmd/server's advanced command runs on the
// global repo with no injection seam (matches the JAB-313 test's precedent).
func TestDomainSet_RunEWiresValidation(t *testing.T) {
	src, err := os.ReadFile("domain_advanced_cmd.go")
	if err != nil {
		t.Fatalf("read domain_advanced_cmd.go: %v", err)
	}
	s := string(src)
	// Pin the CALL expression, not the identifier — the definition line
	// `func validateDomainSetInput(nginxDirs, ...` would satisfy a bare-name
	// match while the RunE call was deleted, leaving the hole this test closes.
	if !strings.Contains(s, "validateDomainSetInput(nginxPtr, redirToPtr, redirTypePtr, indexPtr)") {
		t.Fatal("RunE must CALL validateDomainSetInput before Update, or the CLI persists nginx/redirect/index values the HTTP path would reject (JAB-318)")
	}
	if !strings.Contains(s, "d.RedirectAllType = &normRedirectType") {
		t.Fatal("RunE must store the NORMALISED redirect type, not the raw flag — redirects.Compile emits it verbatim as the nginx return code (JAB-318)")
	}
}
