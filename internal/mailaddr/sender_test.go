package mailaddr

import (
	"errors"
	"strings"
	"testing"
)

// A sender address a mailbox trusts (GH #2017) is matched by Stalwart's
// card-exists check: exactly, ignoring case. So the canonical form lowers
// both parts and punycodes the domain, and keeps the local part otherwise
// as typed: a +tag is part of a different address.
func TestCanonicaliseSender(t *testing.T) {
	t.Parallel()
	ok := []struct{ in, want string }{
		{"bob@example.com", "bob@example.com"},
		{"Bob@Example.COM", "bob@example.com"},
		{"news+weekly@x.com", "news+weekly@x.com"},
		{"bounce=abc@mail.x.com", "bounce=abc@mail.x.com"},
		{"a.b-c_d@x.co.uk", "a.b-c_d@x.co.uk"},
		{"info@bücher.de", "info@xn--bcher-kva.de"},
		{strings.Repeat("a", 64) + "@x.com", strings.Repeat("a", 64) + "@x.com"},
	}
	for _, c := range ok {
		got, err := CanonicaliseSender(c.in)
		if err != nil || got != c.want {
			t.Errorf("CanonicaliseSender(%q) = %q, %v; want %q", c.in, got, err, c.want)
			continue
		}
		if again, err := CanonicaliseSender(got); err != nil || again != got {
			t.Errorf("CanonicaliseSender(%q) is not idempotent: %q, %v", got, again, err)
		}
	}

	bad := []struct {
		in   string
		want error
	}{
		{"", ErrEmpty},
		{"bob.example.com", ErrNoAtSign},
		{"a@b@x.com", ErrMultipleAtSigns},
		{"@x.com", ErrLocalEmpty},
		{"bob@", ErrDomainEmpty},
		{"bob@localhost", ErrDomainNoDot},
		{strings.Repeat("a", 65) + "@x.com", ErrLocalTooLong},
		{"bücher@x.com", ErrLocalNonASCII},
		{"a b@x.com", ErrSenderLocal},
		{"a;b@x.com", ErrSenderLocal},
		{"o'brien@x.com", ErrSenderLocal},
		{"a\"b@x.com", ErrSenderLocal},
		{"a\nb@x.com", ErrSenderLocal},
		{".bob@x.com", ErrSenderLocal},
		{"bob.@x.com", ErrSenderLocal},
		{"b..ob@x.com", ErrSenderLocal},
		{"bob@x .com", ErrDomainShellMeta},
		{" bob@x.com", ErrSenderLocal},
		{"bob@x.com ", ErrDomainShellMeta},
		{"bob@x..com", ErrIDNA},
	}
	for _, c := range bad {
		got, err := CanonicaliseSender(c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("CanonicaliseSender(%q) = %q, %v; want %v", c.in, got, err, c.want)
		}
	}
}
