package commands

import (
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

// GH #1795: buildManagedSieve composes forwards + autoresponder into the single
// active jabali-managed Sieve script that Stalwart actually runs at delivery.

func TestBuildManagedSieve_EmptyIsNoScript(t *testing.T) {
	// No forwards, no enabled autoresponder → "" so the caller DESTROYS the
	// script rather than activating an empty one.
	got, err := buildManagedSieve(nil, managedAutoresponder{})
	if err != nil || got != "" {
		t.Fatalf("want empty/no-error, got %q err %v", got, err)
	}
	// Enabled but no body is not active.
	got, err = buildManagedSieve(nil, managedAutoresponder{Enabled: true, Subject: sp("x")})
	if err != nil || got != "" {
		t.Fatalf("enabled-no-body must be empty, got %q err %v", got, err)
	}
}

func TestBuildManagedSieve_SingleForwardNoCopy(t *testing.T) {
	got, err := buildManagedSieve([]managedForward{{Target: "a@b.com"}}, managedAutoresponder{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "require") {
		t.Fatalf("no copy/vacation → no require line, got:\n%s", got)
	}
	if !strings.Contains(got, `redirect "a@b.com";`) || strings.Contains(got, ":copy") {
		t.Fatalf("want plain redirect, got:\n%s", got)
	}
}

func TestBuildManagedSieve_KeepCopyRequiresCopy(t *testing.T) {
	got, err := buildManagedSieve([]managedForward{{Target: "a@b.com", KeepCopy: true}}, managedAutoresponder{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `require ["copy"];`) || !strings.Contains(got, `redirect :copy "a@b.com";`) {
		t.Fatalf("want copy require + redirect :copy, got:\n%s", got)
	}
}

func TestBuildManagedSieve_MultiTarget(t *testing.T) {
	got, err := buildManagedSieve([]managedForward{
		{Target: "a@b.com"}, {Target: "c@d.com", KeepCopy: true},
	}, managedAutoresponder{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `redirect "a@b.com";`) || !strings.Contains(got, `redirect :copy "c@d.com";`) {
		t.Fatalf("both redirects expected, got:\n%s", got)
	}
}

func TestBuildManagedSieve_AutoresponderOnly(t *testing.T) {
	got, err := buildManagedSieve(nil, managedAutoresponder{Enabled: true, Subject: sp("Away"), TextBody: sp("back monday")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`require ["vacation", "relational", "date"];`, "vacation :mime", `:subject "Away"`, "back monday", "text/plain"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "redirect") || strings.Contains(got, "if ") {
		t.Fatalf("no forwards / no dates → no redirect, no date guard, got:\n%s", got)
	}
}

func TestBuildManagedSieve_Composite(t *testing.T) {
	got, err := buildManagedSieve(
		[]managedForward{{Target: "fwd@x.com", KeepCopy: true}},
		managedAutoresponder{Enabled: true, TextBody: sp("away")},
	)
	if err != nil {
		t.Fatal(err)
	}
	// One script carries BOTH the redirect and the vacation — the single-active-
	// script constraint the whole fix turns on.
	if !strings.Contains(got, `redirect :copy "fwd@x.com";`) || !strings.Contains(got, "vacation :mime") {
		t.Fatalf("composite must carry redirect AND vacation, got:\n%s", got)
	}
	if !strings.Contains(got, `"copy"`) || !strings.Contains(got, `"vacation"`) {
		t.Fatalf("require must list both copy and vacation, got:\n%s", got)
	}
}

func TestBuildManagedSieve_DateWindow(t *testing.T) {
	both, _ := buildManagedSieve(nil, managedAutoresponder{Enabled: true, TextBody: sp("x"), FromDate: sp("2026-01-01T00:00:00Z"), ToDate: sp("2026-01-31T00:00:00Z")})
	if !strings.Contains(both, `allof(currentdate :value "ge" "iso8601" "2026-01-01T00:00:00Z", currentdate :value "le" "iso8601" "2026-01-31T00:00:00Z")`) {
		t.Fatalf("both bounds → allof guard, got:\n%s", both)
	}
	only, _ := buildManagedSieve(nil, managedAutoresponder{Enabled: true, TextBody: sp("x"), FromDate: sp("2026-01-01T00:00:00Z")})
	if !strings.Contains(only, `if currentdate :value "ge" "iso8601" "2026-01-01T00:00:00Z" {`) || strings.Contains(only, "allof") {
		t.Fatalf("single bound → bare currentdate guard, got:\n%s", only)
	}
}

func TestBuildManagedSieve_EscapesSubjectAndBody(t *testing.T) {
	// A subject with a quote+backslash must be escaped, never break the literal
	// or inject a command.
	got, err := buildManagedSieve(nil, managedAutoresponder{Enabled: true, Subject: sp(`ho"la\z`), TextBody: sp(`x"y`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `:subject "ho\"la\\z"`) {
		t.Fatalf("subject must be escaped, got:\n%s", got)
	}
	if strings.Contains(got, `x"y`) { // raw unescaped quote must not appear
		t.Fatalf("body quote not escaped, got:\n%s", got)
	}
}

func TestBuildManagedSieve_RejectsInjectionTarget(t *testing.T) {
	for _, bad := range []string{`a@b";redirect "evil@x`, "a@b c@d", `a@b"x`, "plainstring", "a@b\\c"} {
		if _, err := buildManagedSieve([]managedForward{{Target: bad}}, managedAutoresponder{}); err == nil {
			t.Fatalf("target %q must be rejected", bad)
		}
	}
}
