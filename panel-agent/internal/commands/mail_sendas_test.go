package commands

import (
	"strings"
	"testing"
)

func TestBuildMustMatchSenderExpr_Empty(t *testing.T) {
	// No delegations -> literal "true" (stock value, NOT "!()").
	got, err := buildMustMatchSenderExpr(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "true" {
		t.Fatalf("empty set = %q, want \"true\"", got)
	}
}

func TestBuildMustMatchSenderExpr_Single(t *testing.T) {
	got, err := buildMustMatchSenderExpr([]sendAsPair{
		{DelegateEmail: "support@x.test", GrantorEmail: "sales@x.test"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "!((authenticated_as == 'support@x.test' && sender == 'sales@x.test'))"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildMustMatchSenderExpr_MultiStableAndDeduped(t *testing.T) {
	// Order of input must not change output (stable), and duplicates collapse.
	a, _ := buildMustMatchSenderExpr([]sendAsPair{
		{"support@x.test", "sales@x.test"},
		{"support@x.test", "billing@x.test"},
		{"support@x.test", "sales@x.test"}, // dup
	})
	b, _ := buildMustMatchSenderExpr([]sendAsPair{
		{"support@x.test", "billing@x.test"},
		{"support@x.test", "sales@x.test"},
	})
	if a != b {
		t.Fatalf("not stable/deduped:\n a=%q\n b=%q", a, b)
	}
	if strings.Count(a, "||") != 1 {
		t.Fatalf("want 2 clauses (one ||), got %q", a)
	}
}

func TestBuildMustMatchSenderExpr_SelfDelegationDropped(t *testing.T) {
	got, err := buildMustMatchSenderExpr([]sendAsPair{{"a@x.test", "a@x.test"}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "true" {
		t.Fatalf("self-delegation should drop to \"true\", got %q", got)
	}
}

// Injection guard: an address carrying a quote (expression-breaking / injection)
// must be rejected, never escaped-and-emitted.
func TestBuildMustMatchSenderExpr_RejectsInjection(t *testing.T) {
	for _, bad := range []string{
		"a' || sender == 'anyone@x.test",   // quote-break injection
		"a@x.test' ) || is_empty('",        // parenthesis/quote injection
		"a b@x.test",                       // whitespace
		"a@x",                              // no TLD
		"plainaddress",                     // not an email
	} {
		if _, err := buildMustMatchSenderExpr([]sendAsPair{{DelegateEmail: bad, GrantorEmail: "ok@x.test"}}); err == nil {
			t.Errorf("expected rejection for delegate %q, got nil error", bad)
		}
		if _, err := buildMustMatchSenderExpr([]sendAsPair{{DelegateEmail: "ok@x.test", GrantorEmail: bad}}); err == nil {
			t.Errorf("expected rejection for grantor %q, got nil error", bad)
		}
	}
}

// GH #2267: mailbox domains are stored in punycode (internal/mailaddr), so a
// domain under an internationalised TLD ends in "xn--...". One such pair made
// the whole set unsafe, and no send-as grant on the server reached Stalwart.
func TestBuildMustMatchSenderExpr_AcceptsPunycodeTLD(t *testing.T) {
	got, err := buildMustMatchSenderExpr([]sendAsPair{
		{DelegateEmail: "info@xn--e1afmkfd.xn--p1ai", GrantorEmail: "billing@xn--e1afmkfd.xn--p1ai"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "!((authenticated_as == 'info@xn--e1afmkfd.xn--p1ai' && sender == 'billing@xn--e1afmkfd.xn--p1ai'))"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
