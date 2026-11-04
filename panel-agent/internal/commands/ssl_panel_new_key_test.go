package commands

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// `jabali panel-cert new-key` asks the self-sign verbs for a new key even when
// the self-signed cert on disk is current: an operator whose old key may have
// been copied needs it replaced, and the no-churn rule would keep it forever.

func callPanelSelfSignNewKey(t *testing.T, hostname string) sslPanelSelfSignResponse {
	t.Helper()
	params, _ := json.Marshal(sslPanelSelfSignParams{Hostname: hostname, NewKey: true})
	raw, err := sslPanelSelfSignHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	b, _ := json.Marshal(raw)
	var resp sslPanelSelfSignResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

func callPanelMailSelfSignNewKey(t *testing.T, names ...string) sslPanelMailSelfSignResponse {
	t.Helper()
	params, _ := json.Marshal(sslPanelMailSelfSignParams{Names: names, NewKey: true})
	raw, err := sslPanelMailSelfSignHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	b, _ := json.Marshal(raw)
	var resp sslPanelMailSelfSignResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// assertPairMatches fails unless the cert and key on disk belong together.
func assertPairMatches(t *testing.T, certPath, keyPath string) {
	t.Helper()
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("cert and key on disk don't match: %v", err)
	}
}

func assertNoStagedFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p + ".new"); !os.IsNotExist(err) {
			t.Errorf("staged file %s.new left behind (err=%v)", p, err)
		}
	}
}

func TestPanelSelfSign_NewKeyReplacesCurrentSelfSignedCert(t *testing.T) {
	certPath, keyPath, reloaded := setupPanelSelfSign(t)
	seamHSTSSnippet(t)
	writeTestCert(t, certPath, keyPath, panelSelfSignOrg, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	oldKey, _ := os.ReadFile(keyPath)

	resp := callPanelSelfSignNewKey(t, "host.example.com")

	if !resp.Regenerated {
		t.Fatalf("new_key on a current self-signed cert did not regenerate: %+v", resp)
	}
	if !*reloaded {
		t.Fatalf("new cert written but nginx/panel not reloaded")
	}
	newKey, _ := os.ReadFile(keyPath)
	if string(newKey) == string(oldKey) {
		t.Fatalf("key unchanged after new_key")
	}
	assertPairMatches(t, certPath, keyPath)
	assertNoStagedFiles(t, certPath, keyPath)
	if _, cn := readCertCN_SANs(t, certPath); cn != "host.example.com" {
		t.Fatalf("CN = %q", cn)
	}
}

// A live CA cert is never replaced, even when asked for a new key: Let's
// Encrypt gives it a new key at each renewal.
func TestPanelSelfSign_NewKeyKeepsLiveCACert(t *testing.T) {
	certPath, keyPath, reloaded := setupPanelSelfSign(t)
	writeTestCert(t, certPath, keyPath, "Let's Encrypt", "host.example.com",
		[]string{"host.example.com", "mail.host.example.com"}, time.Now().AddDate(0, 2, 0))
	before, _ := os.ReadFile(keyPath)

	resp := callPanelSelfSignNewKey(t, "host.example.com")

	if resp.Regenerated || *reloaded {
		t.Fatalf("live Let's Encrypt cert replaced or reloaded: %+v reloaded=%v", resp, *reloaded)
	}
	after, _ := os.ReadFile(keyPath)
	if string(before) != string(after) {
		t.Fatalf("live Let's Encrypt key changed")
	}
}

func TestPanelMailSelfSign_NewKeyReplacesCurrentSelfSignedCert(t *testing.T) {
	certPath, keyPath, reloads := setupPanelMailSelfSign(t)
	if resp := callPanelMailSelfSignNewKey(t, "mail.panel.example.com"); !resp.Regenerated {
		t.Fatalf("first run did not write a cert: %+v", resp)
	}
	oldKey, _ := os.ReadFile(keyPath)

	resp := callPanelMailSelfSignNewKey(t, "mail.panel.example.com")

	if !resp.Regenerated || *reloads != 2 {
		t.Fatalf("new_key on a current mail cert: %+v, reloads %d", resp, *reloads)
	}
	newKey, _ := os.ReadFile(keyPath)
	if string(newKey) == string(oldKey) {
		t.Fatalf("mail key unchanged after new_key")
	}
	assertPairMatches(t, certPath, keyPath)
	assertNoStagedFiles(t, certPath, keyPath)
}

func TestPanelMailSelfSign_NewKeyKeepsLiveCACert(t *testing.T) {
	certPath, keyPath, reloads := setupPanelMailSelfSign(t)
	writeTestCert(t, certPath, keyPath, "Let's Encrypt", "mail.panel.example.com",
		[]string{"mail.panel.example.com"}, time.Now().AddDate(0, 2, 0))
	before, _ := os.ReadFile(keyPath)

	resp := callPanelMailSelfSignNewKey(t, "mail.panel.example.com")

	after, _ := os.ReadFile(keyPath)
	if resp.Regenerated || *reloads != 0 || string(before) != string(after) {
		t.Fatalf("live Let's Encrypt mail cert replaced: %+v, reloads %d", resp, *reloads)
	}
}

// A staged file left by an interrupted run must not lend its mode to the new
// key: os.WriteFile keeps an existing file's mode.
func TestSwapInPanelCertPair_IgnoresLeftoverStagedFiles(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "panel.crt"), filepath.Join(dir, "panel.key")
	if err := os.WriteFile(keyPath+".new", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath+".new", 0o644); err != nil {
		t.Fatal(err)
	}

	if err := swapInPanelCertPair(certPath, []byte("CERT"), keyPath, []byte("KEY")); err != nil {
		t.Fatalf("swap: %v", err)
	}

	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("key mode = %o, want 600", mode)
	}
	if got, _ := os.ReadFile(keyPath); string(got) != "KEY" {
		t.Fatalf("key content = %q", got)
	}
	if got, _ := os.ReadFile(certPath); string(got) != "CERT" {
		t.Fatalf("cert content = %q", got)
	}
	assertNoStagedFiles(t, certPath, keyPath)
}
