package api

import "testing"

// A WordPress admin e-mail under an IDN TLD (punycode, e.g. .рф = xn--p1ai)
// was refused by a letters-only TLD rule.
func TestIsValidEmail_IDNTLD(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"admin@example.com", true},
		{"admin@example.xn--p1ai", true},
		{"admin@xn--bcher-kva.xn--p1ai", true},
		{"admin@example.xn--", false},
		{"admin@example", false},
	} {
		if got := isValidEmail(tc.in); got != tc.want {
			t.Errorf("isValidEmail(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
