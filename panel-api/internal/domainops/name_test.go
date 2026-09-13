package domainops

import "testing"

// NormalizeDomainName is the one canonicalizer every create adapter routes
// through (JAB-279 / GH #884). It must lowercase and trim edge whitespace so the
// same input persists the same stored domain regardless of the door it entered.
func TestNormalizeDomainName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"MyDomain.Com", "mydomain.com"},
		{"  spaced.com  ", "spaced.com"},
		{"\tTab.Example.io\n", "tab.example.io"},
		{"already-lower.com", "already-lower.com"},
		{"Sub.Domain.Example.CO.UK", "sub.domain.example.co.uk"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := NormalizeDomainName(tc.in); got != tc.want {
			t.Errorf("NormalizeDomainName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The canonical form is a fixed point: normalizing an already-canonical name
// changes nothing, so re-running the pipeline (e.g. a rename of a stored name)
// is safe.
func TestNormalizeDomainName_Idempotent(t *testing.T) {
	for _, in := range []string{"Example.COM", "  App.Example.io  ", "already.lower.com", ""} {
		once := NormalizeDomainName(in)
		if twice := NormalizeDomainName(once); twice != once {
			t.Errorf("NormalizeDomainName not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}
