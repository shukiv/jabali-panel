package api

// GH #884: domain names must be stored lowercase — a mobile keyboard's
// autocorrected capital produced a domain that was accepted but never
// resolved. normalizeDomainName runs at the create source of truth; assert
// it canonicalizes and that the result passes validateDomainName.

import "testing"

func TestNormalizeDomainName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"MyDomain.Com", "mydomain.com"},
		{"  spaced.com  ", "spaced.com"},
		{"already-lower.com", "already-lower.com"},
		{"Sub.Domain.Example.CO.UK", "sub.domain.example.co.uk"},
	}
	for _, tc := range cases {
		if got := normalizeDomainName(tc.in); got != tc.want {
			t.Errorf("normalizeDomainName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeDomainName_ResultValidates(t *testing.T) {
	// The canonical form of a mixed-case FQDN must satisfy the RFC check,
	// so create() proceeds instead of 400ing on the user's autocorrect.
	for _, in := range []string{"MyDomain.COM", "Santa.Sergia.COM", "  App.Example.io  "} {
		if err := validateDomainName(normalizeDomainName(in)); err != nil {
			t.Errorf("validateDomainName(normalize(%q)) = %v, want nil", in, err)
		}
	}
}
