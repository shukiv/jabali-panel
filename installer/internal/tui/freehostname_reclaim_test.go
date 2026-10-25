package tui

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pressKey(t *testing.T, m Model, k tea.KeyType) Model {
	t.Helper()
	u, _ := m.Update(tea.KeyMsg{Type: k})
	return u.(Model)
}

func hostnameFieldValue(m Model) string {
	for _, f := range m.fields {
		if f.env == "JABALI_HOSTNAME" {
			return f.input.Value()
		}
	}
	return ""
}

// claimedModel returns a model that has just claimed a free hostname and is on
// the config screen, with its credential written under a temp dir.
func claimedModel(t *testing.T) Model {
	t.Helper()
	m := New("/tmp/install.sh", true)
	m.fh.tokenFile = filepath.Join(t.TempDir(), "hostname.env")
	m.fh.email.SetValue("op@example.com")
	m.screen = screenModules
	m = pressKey(t, m, tea.KeyEnter) // → hostname screen
	if m.screen != screenHostname {
		t.Fatalf("screen = %v, want hostname", m.screen)
	}
	u, _ := m.Update(fhClaimedMsg{fqdn: "203-0-113-7.jabalihosted.com", label: "203-0-113-7", token: "abc123"})
	m = u.(Model)
	if m.screen != screenConfig {
		t.Fatalf("after claim: screen = %v, want config (err %q)", m.screen, m.fh.errMsg)
	}
	return m
}

// Going Back from Config to Modules and forward again used to reopen the
// hostname screen on "Free Jabali hostname" with an empty flow: pressing Enter
// meant a second code email and a second claim (field data: 9 labels for one
// email and one IP in two hours). The claim is now kept.
func TestFreeHostname_BackKeepsClaim(t *testing.T) {
	m := claimedModel(t)
	m = pressKey(t, m, tea.KeyEsc)   // config → modules
	m = pressKey(t, m, tea.KeyEnter) // modules → hostname
	if m.screen != screenHostname {
		t.Fatalf("screen = %v, want hostname", m.screen)
	}
	if v := m.View(); !strings.Contains(v, "Keep 203-0-113-7.jabalihosted.com") {
		t.Errorf("hostname screen does not offer to keep the claim:\n%s", stripANSI(v))
	}
	m = pressKey(t, m, tea.KeyEnter)
	if m.fh.stage == fhEmail || m.screen != screenConfig {
		t.Fatalf("Enter on the kept claim: screen %v stage %v — want config, no new claim", m.screen, m.fh.stage)
	}
	if got := hostnameFieldValue(m); got != "203-0-113-7.jabalihosted.com" {
		t.Errorf("hostname field = %q", got)
	}
	if _, err := os.Stat(m.fh.tokenFile); err != nil {
		t.Errorf("credential gone after keeping the claim: %v", err)
	}
}

// Switching to "Enter my own domain" after a claim removes the credential, so
// install.sh does not run the heartbeat for a name this server will not use,
// and clears the field that still holds the free name.
func TestFreeHostname_SwitchToOwnDropsClaim(t *testing.T) {
	m := claimedModel(t)
	tokenFile := m.fh.tokenFile
	m = pressKey(t, m, tea.KeyEsc)
	m = pressKey(t, m, tea.KeyEnter)
	m = pressKey(t, m, tea.KeyDown) // → "Enter my own domain"
	m = pressKey(t, m, tea.KeyEnter)
	if m.screen != screenConfig {
		t.Fatalf("screen = %v, want config", m.screen)
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Errorf("credential still on disk after switching to own domain (err %v)", err)
	}
	if m.fh.fqdn != "" {
		t.Errorf("claim still held: %q", m.fh.fqdn)
	}
	if got := hostnameFieldValue(m); got != "" {
		t.Errorf("hostname field still %q", got)
	}
}

// The installer must reach the hostname service over IPv4 only: the service
// labels a box by its IPv4 source and refuses an IPv6 caller.
func TestFreeHostname_ClientDialsIPv4Only(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ln6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	srv6 := &httptest.Server{Listener: ln6, Config: &http.Server{Handler: ok}}
	srv6.Start()
	defer srv6.Close()
	if resp, err := fhHTTPClient().Get(srv6.URL); err == nil {
		resp.Body.Close()
		t.Fatalf("installer client reached an IPv6-only server: HTTP %d", resp.StatusCode)
	}
	srv4 := httptest.NewServer(ok)
	defer srv4.Close()
	resp, err := fhHTTPClient().Get(srv4.URL)
	if err != nil {
		t.Fatalf("installer client could not reach an IPv4 server: %v", err)
	}
	resp.Body.Close()
}
