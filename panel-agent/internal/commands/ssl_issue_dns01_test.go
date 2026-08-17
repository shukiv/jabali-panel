package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
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
	requireHostMutationAllowed(t) // GH #994: reaches internal/certbot\'s own exec (real `certbot certonly` / ACME) — outside the commands exec seam
	// Passes validation and reaches certbot — which is absent in CI, so a
	// CodeInternal (not CodeInvalidArgument) proves validation let it through.
	_, err := callDNS01(t, `{"cert_name":"wildcard.example.com","sans":["example.com","*.example.com"],"email":"a@b.co"}`)
	if err == nil {
		t.Skip("real certbot present and succeeded — validation clearly passed")
	}
	if ae, ok := err.(*agentwire.AgentError); ok && ae.Code == agentwire.CodeInvalidArgument {
		t.Fatalf("valid params rejected at validation: %s", ae.Message)
	}
}
