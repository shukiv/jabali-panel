package commands

import (
	"context"
	"encoding/json"
	"testing"
)

func callSSLMailDelete(t *testing.T, p sslMailDeleteParams) (sslMailDeleteResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	got, err := sslMailDeleteHandler(context.Background(), raw)
	if err != nil {
		return sslMailDeleteResponse{}, err
	}
	resp, ok := got.(sslMailDeleteResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", got)
	}
	return resp, nil
}

// The lineage never exists in the test environment (sslLERoot=/etc/letsencrypt),
// so cleanupCertbotLineage is a clean no-op and we assert the handler's cert-name
// derivation + validation rather than the certbot side (covered by the box drill
// and cleanupCertbotLineage's own GH #738 tests).
func TestSSLMailDelete_CertNameFromDomain(t *testing.T) {
	resp, err := callSSLMailDelete(t, sslMailDeleteParams{Domain: "foo.test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Ok || resp.CertName != "mail.foo.test" {
		t.Fatalf("got %+v, want ok=true cert_name=mail.foo.test", resp)
	}
}

func TestSSLMailDelete_CertNameFromLineageBasename(t *testing.T) {
	// A re-issued lineage is suffixed; the stored dir's basename wins.
	resp, err := callSSLMailDelete(t, sslMailDeleteParams{
		Domain:      "foo.test",
		LineagePath: "/etc/letsencrypt/live/mail.foo.test-0001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CertName != "mail.foo.test-0001" {
		t.Fatalf("cert_name = %q, want mail.foo.test-0001", resp.CertName)
	}
}

func TestSSLMailDelete_RejectsBadInput(t *testing.T) {
	for _, d := range []string{"", "../etc/passwd", "a b.test", "foo.test;rm -rf"} {
		if _, err := sslMailDeleteHandler(context.Background(), mustJSON(t, sslMailDeleteParams{Domain: d})); err == nil {
			t.Errorf("domain %q: expected error, got nil", d)
		}
	}
}
