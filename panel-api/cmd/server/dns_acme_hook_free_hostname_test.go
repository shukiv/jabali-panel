package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/freehostname"
)

type hostnameServiceCall struct {
	path string
	body map[string]string
}

// fakeHostnameService stands in for api.jabalihosted.com and points the
// hook's credential file, client and DNS checks at it.
func fakeHostnameService(t *testing.T, fqdn string) *[]hostnameServiceCall {
	t.Helper()
	calls := &[]hostnameServiceCall{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		*calls = append(*calls, hostnameServiceCall{r.URL.Path, b})
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	env := filepath.Join(t.TempDir(), "hostname.env")
	body := "LABEL=x\nFQDN=" + fqdn + "\nEMAIL=op@example.com\nTOKEN=tok123\nAPI=" + srv.URL + "\n"
	if err := os.WriteFile(env, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	prevPath, prevHTTP, prevNS, prevTXT, prevGrace := freehostname.EnvPath, freeHostnameHTTP, freeHostnameLookupNS, freeHostnameLookupTXT, freeHostnameGrace
	freehostname.EnvPath = env
	freeHostnameHTTP = srv.Client
	freeHostnameLookupNS = func(context.Context, string) ([]string, bool) { return []string{"ns.test"}, true }
	freeHostnameLookupTXT = func(_ context.Context, _, _ string) ([]string, error) {
		for _, c := range *calls {
			if c.path == "/v1/acme/present" {
				return []string{c.body["txt"]}, nil
			}
		}
		return nil, nil
	}
	freeHostnameGrace = 0
	t.Cleanup(func() {
		freehostname.EnvPath, freeHostnameHTTP, freeHostnameLookupNS, freeHostnameLookupTXT, freeHostnameGrace = prevPath, prevHTTP, prevNS, prevTXT, prevGrace
	})
	return calls
}

// The preview wildcard *.<label>.jabalihosted.com reaches the hook as its
// bare name. The challenge goes through the hostname service with the box's
// token — never the panel DNS tables or Cloudflare, which cannot answer for
// jabalihosted.com.
func TestAcmeHook_FreeHostnameGoesThroughService(t *testing.T) {
	calls := fakeHostnameService(t, "203-0-113-7.jabalihosted.com")
	ctx := context.Background()

	if err := acmeHookAuth(ctx, "wildcard.203-0-113-7.jabalihosted.com", "203-0-113-7.jabalihosted.com", "val-1"); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := acmeHookCleanup(ctx, "wildcard.203-0-113-7.jabalihosted.com", "203-0-113-7.jabalihosted.com", "val-1"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	got := *calls
	if len(got) != 2 ||
		got[0].path != "/v1/acme/present" || got[0].body["token"] != "tok123" || got[0].body["txt"] != "val-1" ||
		got[1].path != "/v1/acme/cleanup" || got[1].body["token"] != "tok123" {
		t.Fatalf("service calls = %+v", got)
	}
}

// Only the box's own free hostname takes that route; every other name, and a
// box with no free hostname, carries on to the panel DNS / Cloudflare routes.
func TestFreeHostnameCredentialFor(t *testing.T) {
	fakeHostnameService(t, "203-0-113-7.jabalihosted.com")
	for domain, want := range map[string]bool{
		"203-0-113-7.jabalihosted.com":      true,
		"203-0-113-7.JABALIHOSTED.com":      true,
		"198-51-100-1.jabalihosted.com":     false, // another box's label
		"mail.203-0-113-7.jabalihosted.com": false, // the service only writes the label's own name
		"example.com":                       false,
	} {
		cred, err := freeHostnameCredentialFor(domain)
		if err != nil {
			t.Errorf("%s: %v", domain, err)
		}
		if (cred != nil) != want {
			t.Errorf("%s: credential = %v, want route=%v", domain, cred != nil, want)
		}
	}

	freehostname.EnvPath = filepath.Join(t.TempDir(), "absent.env")
	if cred, err := freeHostnameCredentialFor("203-0-113-7.jabalihosted.com"); cred != nil || err != nil {
		t.Errorf("no credential file: cred=%v err=%v, want neither", cred, err)
	}
}

// A broken credential file fails the free-hostname name loudly, and leaves
// every other domain's issuance alone.
func TestFreeHostnameCredentialFor_BrokenFile(t *testing.T) {
	fakeHostnameService(t, "203-0-113-7.jabalihosted.com")
	bad := filepath.Join(t.TempDir(), "hostname.env")
	if err := os.WriteFile(bad, []byte("FQDN=203-0-113-7.jabalihosted.com\nTOKEN=$(id)\nAPI=https://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	freehostname.EnvPath = bad
	if _, err := freeHostnameCredentialFor("203-0-113-7.jabalihosted.com"); err == nil {
		t.Error("broken credential: want an error for the free hostname")
	}
	if cred, err := freeHostnameCredentialFor("example.com"); cred != nil || err != nil {
		t.Errorf("broken credential must not affect example.com: cred=%v err=%v", cred, err)
	}
}
