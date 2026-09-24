package domainops

import (
	"errors"
	"reflect"
	"testing"
)

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

// AncestorDomains backs the cross-tenant subdomain-hijack guard (GH #1789): for
// the name being claimed it yields the registrable parent zones another tenant
// could already own. It must be label-boundary aware, most-specific first, and
// must NOT emit the bare TLD (no domain row can hold "com") nor the name itself.
func TestAncestorDomains(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Deeper name → every strict parent down to the second-level domain.
		{"a.b.example.com", []string{"b.example.com", "example.com"}},
		{"evil.example.com", []string{"example.com"}},
		// A registrable second-level name has no registrable ancestor (its only
		// parent is the bare TLD, which is excluded).
		{"example.com", nil},
		// Multi-label public suffixes: the helper is suffix-list-agnostic, so it
		// still walks label boundaries (the guard compares against real rows, so
		// a nonexistent "co.uk" row simply never matches).
		{"shop.example.co.uk", []string{"example.co.uk", "co.uk"}},
		// Degenerate inputs never panic and never invent a parent.
		{"com", nil},
		{"", nil},
	}
	for _, tc := range cases {
		if got := AncestorDomains(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("AncestorDomains(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestValidateDomainName is the shared FQDN gate every create/rename/alias
// adapter routes through (JAB-279 AC2/AC6). Each row pins the FIRST failing
// reason (the sentinel), so both the HTTP and CLI adapters can map a specific
// message; a valid name returns nil. The ordering (empty, whitespace, length,
// HTML, path, FQDN) is part of the contract — a name that trips two checks must
// report the earlier reason on every door.
func TestValidateDomainName(t *testing.T) {
	over253 := ""
	for len(over253) <= 253 {
		over253 += "aaaaaaaaaa.example.com."
	}
	over253 += "example.com" // valid-charset name that is simply over the 253-char cap

	cases := []struct {
		name string
		in   string
		want error // nil = accepted
	}{
		{"valid two-label", "example.com", nil},
		{"valid subdomain", "a.b.example.com", nil},
		{"valid hyphen", "my-site.co.uk", nil},
		{"empty", "", ErrDomainNameEmpty},
		{"blank", "   ", ErrDomainNameEmpty},
		{"space", "exa mple.com", ErrDomainNameWhitespace},
		{"tab", "example\t.com", ErrDomainNameWhitespace},
		{"carriage return", "example.com\r", ErrDomainNameWhitespace},
		{"too long", over253, ErrDomainNameTooLong},
		{"html tag", "<script>.com", ErrDomainNameHTML},
		{"dotdot", "a..b.com", ErrDomainNameTraversal},
		{"slash", "example.com/evil", ErrDomainNameTraversal},
		{"backslash", "example\\com", ErrDomainNameTraversal},
		{"bare hostname", "localhost", ErrDomainNameNotFQDN},
		{"ip literal", "192.168.1.1", ErrDomainNameNotFQDN},
		{"numeric tld", "example.123", ErrDomainNameNotFQDN},
		{"single letter tld", "example.c", ErrDomainNameNotFQDN},
		{"trailing dot", "example.com.", ErrDomainNameNotFQDN},
		{"underscore", "ex_ample.com", ErrDomainNameNotFQDN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateDomainName(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("ValidateDomainName(%q) = %v, want nil (accepted)", tc.in, got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("ValidateDomainName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
