package models

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateMailHostname covers each acceptance and rejection arm. Every
// invalid case names the sentinel it must return so neutralizing a single
// validator arm reddens exactly the cases that depend on it (JAB-390).
func TestValidateMailHostname(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string // expected normalized host on success
		wantErr error  // nil = expect success
	}{
		{"simple fqdn", "mail.example.com", "mail.example.com", nil},
		{"trims surrounding whitespace", "  mail.example.com\t", "mail.example.com", nil},
		{"lowercases", "Mail.Example.COM", "mail.example.com", nil},
		{"deep subdomain", "a.b.c.example.com", "a.b.c.example.com", nil},
		{"digits and hyphens in labels", "mx-1.ex-ample.com", "mx-1.ex-ample.com", nil},

		{"empty", "", "", ErrMailHostnameEmpty},
		{"only whitespace", "   ", "", ErrMailHostnameEmpty},
		{"internal space", "mail. example.com", "", ErrMailHostnameWhitespace},
		{"internal tab", "mail\texample.com", "", ErrMailHostnameWhitespace},
		{"scheme", "https://mail.example.com", "", ErrMailHostnameNotBare},
		{"path", "mail.example.com/inbox", "", ErrMailHostnameNotBare},
		{"userinfo", "user@mail.example.com", "", ErrMailHostnameNotBare},
		{"backslash", "mail.example.com\\x", "", ErrMailHostnameNotBare},
		{"query", "mail.example.com?a=1", "", ErrMailHostnameNotBare},
		{"fragment", "mail.example.com#f", "", ErrMailHostnameNotBare},
		{"wildcard", "*.example.com", "", ErrMailHostnameWildcard},
		{"port", "mail.example.com:993", "", ErrMailHostnamePort},
		{"leading dot", ".mail.example.com", "", ErrMailHostnameDot},
		{"trailing dot", "mail.example.com.", "", ErrMailHostnameDot},
		{"single label", "localhost", "", ErrMailHostnameNotFQDN},
		{"ipv4 literal", "192.0.2.10", "", ErrMailHostnameIP},
		{"empty inner label", "mail..example.com", "", ErrMailHostnameLabel},
		{"leading hyphen label", "mail.-example.com", "", ErrMailHostnameLabel},
		{"trailing hyphen label", "mail.example-.com", "", ErrMailHostnameLabel},
		{"underscore not allowed", "mail_x.example.com", "", ErrMailHostnameLabel},
		{"label over 63", strings.Repeat("a", 64) + ".example.com", "", ErrMailHostnameLabel},
		{"host over 253", strings.Repeat("a.", 130) + "example.com", "", ErrMailHostnameTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateMailHostname(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ValidateMailHostname(%q) err = %v, want %v", tc.in, err, tc.wantErr)
				}
				if got != "" {
					t.Errorf("ValidateMailHostname(%q) returned %q on error, want empty", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateMailHostname(%q) unexpected err = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidateMailHostname(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEffectiveMailHostname pins the resolver: unset or invalid override
// falls back to the derived mail.<hostname>; a valid override wins and is
// normalized. The nil→derived and invalid→derived arms ARE the
// "upgraded/corrupt rows preserve today's behaviour" guarantee (JAB-390).
func TestEffectiveMailHostname(t *testing.T) {
	sp := func(s string) *string { return &s }
	cases := []struct {
		name     string
		override *string
		host     string
		want     string
	}{
		{"nil override derives", nil, "panel.example.com", "mail.panel.example.com"},
		{"nil override empty host", nil, "", ""},
		{"valid override wins", sp("mail.custom.io"), "panel.example.com", "mail.custom.io"},
		{"valid override normalized", sp("  Mail.Custom.IO "), "panel.example.com", "mail.custom.io"},
		{"empty-string override falls back", sp(""), "panel.example.com", "mail.panel.example.com"},
		{"whitespace override falls back", sp("   "), "panel.example.com", "mail.panel.example.com"},
		{"invalid override falls back (fail-safe)", sp("https://evil/x"), "panel.example.com", "mail.panel.example.com"},
		{"wildcard override falls back", sp("*.custom.io"), "panel.example.com", "mail.panel.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveMailHostname(tc.override, tc.host); got != tc.want {
				t.Errorf("EffectiveMailHostname(%v, %q) = %q, want %q", tc.override, tc.host, got, tc.want)
			}
		})
	}
}

// TestAppliedMailHostname pins which stored values count as an applied mail
// hostname: only a value that passes validation, returned normalized. Anything
// else means the derived default is in effect (JAB-390).
func TestAppliedMailHostname(t *testing.T) {
	sp := func(s string) *string { return &s }
	cases := []struct {
		name    string
		stored  *string
		want    string
		applied bool
	}{
		{"NULL is not applied", nil, "", false},
		{"empty is not applied", sp(""), "", false},
		{"invalid is not applied", sp("https://evil/x"), "", false},
		{"IP literal is not applied", sp("192.0.2.1"), "", false},
		{"valid is applied", sp("mx.example.net"), "mx.example.net", true},
		{"valid is normalized", sp("  MX.Example.NET "), "mx.example.net", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AppliedMailHostname(tc.stored)
			if got != tc.want || ok != tc.applied {
				t.Errorf("AppliedMailHostname(%v) = (%q, %v), want (%q, %v)", tc.stored, got, ok, tc.want, tc.applied)
			}
		})
	}
}
