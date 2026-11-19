package commands

import "testing"

// IDN TLDs travel in punycode (.рф = xn--p1ai, .ישראל = xn--4dbrk0ce). The
// migration probe and the Let's Encrypt contact e-mail used a letters-only TLD
// rule, so a domain (or an admin address) under an IDN TLD was refused.
func TestProbeDomainRe_IDNTLD(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"example.com", true},
		{"example.xn--p1ai", true},
		{"xn----7sbbhn.xn--4dbrk0ce", true},
		{"example.xn--", false},
		{"example.123", false},
		{"Example.com", false}, // the probe takes lower-case names only
	} {
		if got := probeDomainRe.MatchString(tc.in); got != tc.want {
			t.Errorf("probeDomainRe(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSSLIssueEmailRegex_IDNTLD(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"admin@example.com", true},
		{"admin@example.xn--p1ai", true},
		{"admin@example.xn--", false},
		{"admin@example", false},
	} {
		if got := emailRegex.MatchString(tc.in); got != tc.want {
			t.Errorf("emailRegex(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
