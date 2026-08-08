package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreeHostname_RegisterClaimWritesCredential(t *testing.T) {
	var claimBody string
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/register":
			w.Write([]byte(`{"ok":true}`))
		case "/v1/claim":
			b := make([]byte, r.ContentLength)
			r.Body.Read(b)
			claimBody = string(b)
			w.Write([]byte(`{"label":"203-0-113-7","fqdn":"203-0-113-7.jabalihosted.com","token":"tok-xyz"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer svc.Close()
	t.Setenv("JABALI_HOSTNAME_API", svc.URL)

	fh := newFHModel()
	fh.tokenFile = filepath.Join(t.TempDir(), "hostname.env")
	fh.email.SetValue("op@example.com")

	// register → expect a code-sent message
	if msg := fh.registerCmd("op@example.com")(); msg == nil {
		t.Fatal("register returned nil msg")
	} else if _, ok := msg.(fhCodeSentMsg); !ok {
		t.Fatalf("register msg = %T, want fhCodeSentMsg", msg)
	}

	// claim → expect a claimed message with the fqdn + token
	msg := fh.claimCmd("op@example.com", "123456")()
	claimed, ok := msg.(fhClaimedMsg)
	if !ok {
		t.Fatalf("claim msg = %T, want fhClaimedMsg", msg)
	}
	if claimed.fqdn != "203-0-113-7.jabalihosted.com" || claimed.token != "tok-xyz" {
		t.Fatalf("claimed = %+v", claimed)
	}
	if !strings.Contains(claimBody, "123456") || !strings.Contains(claimBody, "op@example.com") {
		t.Errorf("claim body did not carry email+code: %s", claimBody)
	}

	// writeCredential persists 0600 with the token
	fh.fqdn, fh.label, fh.token = claimed.fqdn, claimed.label, claimed.token
	if err := fh.writeCredential(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(fh.tokenFile)
	if !strings.Contains(string(data), "TOKEN=tok-xyz") || !strings.Contains(string(data), "FQDN=203-0-113-7.jabalihosted.com") {
		t.Errorf("credential missing fields:\n%s", data)
	}
	fi, _ := os.Stat(fh.tokenFile)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("credential mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestFreeHostname_ClaimBadCodeSurfacesError(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"code_invalid","message":"code wrong or expired"}`))
	}))
	defer svc.Close()
	t.Setenv("JABALI_HOSTNAME_API", svc.URL)

	fh := newFHModel()
	msg := fh.claimCmd("op@example.com", "000000")()
	e, ok := msg.(fhErrMsg)
	if !ok {
		t.Fatalf("msg = %T, want fhErrMsg", msg)
	}
	if !strings.Contains(e.msg, "code wrong or expired") {
		t.Errorf("error did not surface the reason: %q", e.msg)
	}
}

// The claimed FQDN must land in the hostname config field so install.sh gets a
// normal JABALI_HOSTNAME (no bash free-hostname path on the TUI install).
func TestFreeHostname_SetsHostnameField(t *testing.T) {
	fields := newConfigFields("")
	setHostnameField(fields, "203-0-113-7.jabalihosted.com")
	var got string
	for _, f := range fields {
		if f.env == "JABALI_HOSTNAME" {
			got = f.input.Value()
		}
	}
	if got != "203-0-113-7.jabalihosted.com" {
		t.Errorf("hostname field = %q", got)
	}
}
