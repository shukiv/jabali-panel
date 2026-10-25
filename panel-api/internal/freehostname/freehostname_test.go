package freehostname

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hostname.env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodEnv = "LABEL=203-0-113-7\nFQDN=203-0-113-7.jabalihosted.com\nEMAIL=op@example.com\nTOKEN=abc123def\nAPI=https://api.jabalihosted.com\n"

func TestReadCredential(t *testing.T) {
	c, err := ReadCredential(writeEnv(t, goodEnv))
	if err != nil {
		t.Fatal(err)
	}
	if c.Label != "203-0-113-7" || c.FQDN != "203-0-113-7.jabalihosted.com" || c.Token != "abc123def" || c.API != "https://api.jabalihosted.com" {
		t.Fatalf("credential = %+v", c)
	}

	// First line per key wins, as the shell readers' grep -m1 does.
	c, err = ReadCredential(writeEnv(t, goodEnv+"TOKEN=second\nFQDN=x-y.jabalihosted.com\n"))
	if err != nil || c.Token != "abc123def" || c.FQDN != "203-0-113-7.jabalihosted.com" {
		t.Fatalf("duplicate keys: %+v, %v", c, err)
	}
}

func TestReadCredential_Missing(t *testing.T) {
	if _, err := ReadCredential(filepath.Join(t.TempDir(), "absent.env")); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

// Values come from the hosted service and are used by root: anything
// outside the expected shapes is refused, never cleaned up and used.
func TestReadCredential_RefusesBadValues(t *testing.T) {
	for name, env := range map[string]string{
		"token with shell":  strings.Replace(goodEnv, "TOKEN=abc123def", "TOKEN=$(id)", 1),
		"empty token":       strings.Replace(goodEnv, "TOKEN=abc123def", "TOKEN=", 1),
		"foreign fqdn":      strings.Replace(goodEnv, "FQDN=203-0-113-7.jabalihosted.com", "FQDN=evil.example.com", 1),
		"two-label fqdn":    strings.Replace(goodEnv, "FQDN=203-0-113-7.jabalihosted.com", "FQDN=a.b.jabalihosted.com", 1),
		"http api":          strings.Replace(goodEnv, "API=https://", "API=http://", 1),
		"api with userinfo": strings.Replace(goodEnv, "API=https://", "API=https://u:p@", 1),
		"api with space":    strings.Replace(goodEnv, "API=https://api.jabalihosted.com", "API=https://api.jabalihosted.com x", 1),
		"no api":            strings.Replace(goodEnv, "API=https://api.jabalihosted.com\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if c, err := ReadCredential(writeEnv(t, env)); err == nil {
				t.Fatalf("accepted %+v", c)
			}
		})
	}
}

func TestChallengeCalls(t *testing.T) {
	type call struct {
		path string
		body map[string]string
	}
	var calls []call
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		calls = append(calls, call{r.URL.Path, b})
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := &Credential{FQDN: "203-0-113-7.jabalihosted.com", Token: "abc123def", API: srv.URL}
	if err := c.PresentChallenge(context.Background(), srv.Client(), "val-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.ClearChallenge(context.Background(), srv.Client()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 ||
		calls[0].path != "/v1/acme/present" || calls[0].body["token"] != "abc123def" || calls[0].body["txt"] != "val-1" ||
		calls[1].path != "/v1/acme/cleanup" || calls[1].body["token"] != "abc123def" {
		t.Fatalf("calls = %+v", calls)
	}
}

// A refused call names the service's reason and never carries the token.
func TestChallengeErrorKeepsTokenOut(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad_token","message":"unknown token"}`))
	}))
	defer srv.Close()
	c := &Credential{Token: "secret-token-value", API: srv.URL}
	err := c.PresentChallenge(context.Background(), srv.Client(), "v")
	if err == nil || !strings.Contains(err.Error(), "unknown token") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("token leaked into the error: %v", err)
	}
}

func TestHTTPClientDialsIPv4Only(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ln6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	srv6 := &httptest.Server{Listener: ln6, Config: &http.Server{Handler: ok}}
	srv6.Start()
	defer srv6.Close()
	if resp, err := HTTPClient().Get(srv6.URL); err == nil {
		resp.Body.Close()
		t.Fatalf("reached an IPv6-only server: HTTP %d", resp.StatusCode)
	}
	srv4 := httptest.NewServer(ok)
	defer srv4.Close()
	resp, err := HTTPClient().Get(srv4.URL)
	if err != nil {
		t.Fatalf("could not reach an IPv4 server: %v", err)
	}
	resp.Body.Close()
}
