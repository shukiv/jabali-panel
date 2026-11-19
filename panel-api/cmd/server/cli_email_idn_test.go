package main

import "testing"

// `jabali user create` refused an e-mail under an IDN TLD (punycode, e.g.
// .рф = xn--p1ai): its TLD rule was letters only.
func TestCLIValidEmail_IDNTLD(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"owner@example.com", true},
		{"owner@example.xn--p1ai", true},
		{"owner@xn----7sbbhn.xn--4dbrk0ce", true},
		{"owner@example.xn--", false},
		{"owner@example.123", false},
	} {
		if got := cliValidEmail(tc.in); got != tc.want {
			t.Errorf("cliValidEmail(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
