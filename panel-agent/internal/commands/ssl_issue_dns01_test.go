package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/certbot"
)

// Validation-layer tests only — the certbot invocation itself is covered
// by certbot.TestIssueDNS01* against a fake binary.

func callDNS01(t *testing.T, params string) (any, error) {
	t.Helper()
	return sslIssueDNS01Handler(context.Background(), json.RawMessage(params))
}

func wantInvalidArgument(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	ae, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T: %v", err, err)
	}
	if ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s (%s)", ae.Code, ae.Message)
	}
	if fragment != "" && !strings.Contains(ae.Message, fragment) {
		t.Errorf("error %q does not mention %q", ae.Message, fragment)
	}
}

func TestSSLIssueDNS01_RejectsWildcardCertName(t *testing.T) {
	_, err := callDNS01(t, `{"cert_name":"*.example.com","sans":["*.example.com"],"email":"a@b.co"}`)
	wantInvalidArgument(t, err, "cert_name")
}

func TestSSLIssueDNS01_RejectsBadSANs(t *testing.T) {
	cases := []string{
		`{"cert_name":"wildcard.example.com","sans":[],"email":"a@b.co"}`,
		`{"cert_name":"wildcard.example.com","sans":["*.*.example.com"],"email":"a@b.co"}`,
		`{"cert_name":"wildcard.example.com","sans":["*"],"email":"a@b.co"}`,
		`{"cert_name":"wildcard.example.com","sans":["exa mple.com"],"email":"a@b.co"}`,
	}
	for _, c := range cases {
		if _, err := callDNS01(t, c); err == nil {
			t.Errorf("params %s: expected validation error", c)
		}
	}
}

func TestSSLIssueDNS01_RejectsBadEmail(t *testing.T) {
	_, err := callDNS01(t, `{"cert_name":"wildcard.example.com","sans":["*.example.com"],"email":"nope"}`)
	wantInvalidArgument(t, err, "email")
}

func TestSSLIssueDNS01_AcceptsSingleWildcardSAN(t *testing.T) {
	// GH #1160: internal/certbot's exec is now test-safe (NewRunner defaults
	// Binary to a no-op stub under `go test`), so this no longer reaches real
	// ACME and the stopgap requireHostMutationAllowed gate is gone. Validation
	// passes and reaches the (stubbed) certbot: a non-CodeInvalidArgument error
	// (or success) proves the wildcard SAN was let through.
	_, err := callDNS01(t, `{"cert_name":"wildcard.example.com","sans":["example.com","*.example.com"],"email":"a@b.co"}`)
	if err == nil {
		t.Skip("real certbot present and succeeded — validation clearly passed")
	}
	if ae, ok := err.(*agentwire.AgentError); ok && ae.Code == agentwire.CodeInvalidArgument {
		t.Fatalf("valid params rejected at validation: %s", ae.Message)
	}
}

// GH #2032: the hooks carry the lineage so the panel can tell whose
// certificate a challenge belongs to. certbot stores these strings in the
// renewal conf and runs them through the shell on every renewal.
func TestACMEDNSHooks_CarryTheLineage(t *testing.T) {
	auth, cleanup := acmeDNSHooks("example.com")
	if auth != "JABALI_ACME_LINEAGE=example.com /usr/local/bin/jabali-panel dns acme-hook auth" {
		t.Errorf("auth hook = %q", auth)
	}
	if cleanup != "JABALI_ACME_LINEAGE=example.com /usr/local/bin/jabali-panel dns acme-hook cleanup" {
		t.Errorf("cleanup hook = %q", cleanup)
	}
}

// The lineage lands in a shell command line, so a cert_name that could
// break out of it never reaches certbot.
func TestSSLIssueDNS01_RejectsShellInCertName(t *testing.T) {
	for _, name := range []string{"example.com;reboot", "example.com reboot", "$(id).example.com", "example.com`id`", "a.com|x", "a.com&&x"} {
		_, err := callDNS01(t, `{"cert_name":`+strconvQuote(name)+`,"sans":["example.com"],"email":"a@b.co"}`)
		wantInvalidArgument(t, err, "cert_name")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// certbot is handed the hooks that carry the lineage, so the renewal conf
// it writes keeps them.
func TestSSLIssueDNS01_PassesTheLineageHooks(t *testing.T) {
	orig := issueDNS01
	t.Cleanup(func() { issueDNS01 = orig })
	var gotAuth, gotCleanup string
	issueDNS01 = func(_ string, _ []string, _ string, _ bool, authHook, cleanupHook string) (*certbot.Result, error) {
		gotAuth, gotCleanup = authHook, cleanupHook
		return &certbot.Result{}, nil
	}
	if _, err := callDNS01(t, `{"cert_name":"example.com","sans":["example.com","www.example.com"],"email":"a@b.co"}`); err != nil {
		t.Fatal(err)
	}
	if want, _ := acmeDNSHooks("example.com"); gotAuth != want {
		t.Errorf("auth hook = %q, want %q", gotAuth, want)
	}
	if _, want := acmeDNSHooks("example.com"); gotCleanup != want {
		t.Errorf("cleanup hook = %q, want %q", gotCleanup, want)
	}
}
