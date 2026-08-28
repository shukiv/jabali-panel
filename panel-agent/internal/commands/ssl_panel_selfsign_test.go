package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCert writes a self-signed cert+key with the given issuer/subject
// Organization, CN, and DNS SANs — a test double for "a cert already on
// disk". org=="" produces a cert whose issuer Organization is empty.
func writeTestCert(t *testing.T, certPath, keyPath, org, cn string, dnsSANs []string, notAfter time.Time) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	subj := pkix.Name{CommonName: cn}
	if org != "" {
		subj.Organization = []string{org}
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               subj,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsSANs,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// setupPanelSelfSign points the package paths at a temp dir and stubs the
// reload/restart seam, returning the paths and a pointer to a "reload was
// called" flag. Restores the globals on cleanup.
func setupPanelSelfSign(t *testing.T) (certPath, keyPath string, reloaded *bool) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "panel.crt")
	keyPath = filepath.Join(dir, "panel.key")

	oc, ok, or := panelSelfSignCertPath, panelSelfSignKeyPath, panelSelfSignReloadFn
	called := false
	panelSelfSignCertPath = certPath
	panelSelfSignKeyPath = keyPath
	panelSelfSignReloadFn = func(context.Context) { called = true }
	t.Cleanup(func() {
		panelSelfSignCertPath, panelSelfSignKeyPath, panelSelfSignReloadFn = oc, ok, or
	})
	return certPath, keyPath, &called
}

func callPanelSelfSign(t *testing.T, hostname, ip string) (sslPanelSelfSignResponse, error) {
	t.Helper()
	params, _ := json.Marshal(sslPanelSelfSignParams{Hostname: hostname, IP: ip})
	raw, err := sslPanelSelfSignHandler(context.Background(), params)
	if err != nil {
		return sslPanelSelfSignResponse{}, err
	}
	b, _ := json.Marshal(raw)
	var resp sslPanelSelfSignResponse
	if e := json.Unmarshal(b, &resp); e != nil {
		t.Fatalf("unmarshal resp: %v", e)
	}
	return resp, nil
}

func readCertCN_SANs(t *testing.T, certPath string) (*x509.Certificate, string) {
	t.Helper()
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert, cert.Subject.CommonName
}

// TestPanelSelfSign_PreservesLECert is the #1 regression guard (advisor):
// a deployed Let's Encrypt cert (issuer O="Let's Encrypt") must NEVER be
// clobbered by the self-signed regen, even when its CN doesn't match the
// requested hostname.
func TestPanelSelfSign_PreservesLECert(t *testing.T) {
	certPath, keyPath, reloaded := setupPanelSelfSign(t)
	writeTestCert(t, certPath, keyPath, "Let's Encrypt", "old.example.com",
		[]string{"old.example.com"}, time.Now().AddDate(0, 3, 0))
	before, _ := os.ReadFile(certPath)

	resp, err := callPanelSelfSign(t, "new.example.com", "")
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp.Regenerated {
		t.Fatalf("LE cert was regenerated — must be preserved")
	}
	after, _ := os.ReadFile(certPath)
	if string(before) != string(after) {
		t.Fatalf("LE cert bytes changed — must be untouched")
	}
	if *reloaded {
		t.Fatalf("reload/restart fired for a preserved cert (needless churn)")
	}
}

// TestPanelSelfSign_RegeneratesOnHostnameDrift: a self-signed cert for the
// old hostname is replaced with one for the new hostname (CN + mail SAN),
// and services are reloaded.
func TestPanelSelfSign_RegeneratesOnHostnameDrift(t *testing.T) {
	certPath, keyPath, reloaded := setupPanelSelfSign(t)
	writeTestCert(t, certPath, keyPath, panelSelfSignOrg, "old.example.com",
		[]string{"old.example.com", "mail.old.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))

	resp, err := callPanelSelfSign(t, "new.example.com", "203.0.113.9")
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if !resp.Regenerated {
		t.Fatalf("expected regenerated=true, got reason %q", resp.Reason)
	}
	if !*reloaded {
		t.Fatalf("reload/restart was not fired after regen")
	}
	cert, cn := readCertCN_SANs(t, certPath)
	if cn != "new.example.com" {
		t.Fatalf("CN not updated: %q", cn)
	}
	wantSAN := map[string]bool{"new.example.com": false, "mail.new.example.com": false, "localhost": false}
	for _, d := range cert.DNSNames {
		if _, ok := wantSAN[d]; ok {
			wantSAN[d] = true
		}
	}
	for name, seen := range wantSAN {
		if !seen {
			t.Fatalf("missing DNS SAN %q (have %v)", name, cert.DNSNames)
		}
	}
	// IP SANs: loopback + the passed public IP.
	var haveLoop, havePub bool
	for _, ip := range cert.IPAddresses {
		if ip.String() == "127.0.0.1" {
			haveLoop = true
		}
		if ip.String() == "203.0.113.9" {
			havePub = true
		}
	}
	if !haveLoop || !havePub {
		t.Fatalf("IP SANs wrong: %v", cert.IPAddresses)
	}
	// Issuer Organization must carry our marker so a later run treats it as
	// regenerable (and install.sh's preserve-guard classifies it correctly).
	if got := panelCertIssuerOrg(cert); got != panelSelfSignOrg {
		t.Fatalf("issuer O = %q, want %q", got, panelSelfSignOrg)
	}
}

// TestPanelSelfSign_NoChurnWhenCovered: an unexpired self-signed cert that
// already covers the FQDN is left as-is and no reload fires.
func TestPanelSelfSign_NoChurnWhenCovered(t *testing.T) {
	certPath, keyPath, reloaded := setupPanelSelfSign(t)
	writeTestCert(t, certPath, keyPath, panelSelfSignOrg, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	before, _ := os.ReadFile(certPath)

	resp, err := callPanelSelfSign(t, "host.example.com", "")
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp.Regenerated {
		t.Fatalf("cert regenerated despite already covering the FQDN")
	}
	if *reloaded {
		t.Fatalf("reload fired for a no-op")
	}
	after, _ := os.ReadFile(certPath)
	if string(before) != string(after) {
		t.Fatalf("cert bytes changed on a no-op")
	}
}

// TestPanelSelfSign_RegeneratesWhenMissing: no cert on disk → generate one.
func TestPanelSelfSign_RegeneratesWhenMissing(t *testing.T) {
	certPath, _, reloaded := setupPanelSelfSign(t)
	resp, err := callPanelSelfSign(t, "fresh.example.com", "")
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if !resp.Regenerated {
		t.Fatalf("expected regenerated=true for missing cert")
	}
	if !*reloaded {
		t.Fatalf("reload not fired")
	}
	if _, cn := readCertCN_SANs(t, certPath); cn != "fresh.example.com" {
		t.Fatalf("CN = %q", cn)
	}
}

func TestPanelSelfSign_InvalidHostname(t *testing.T) {
	setupPanelSelfSign(t)
	if _, err := callPanelSelfSign(t, "not a host", ""); err == nil {
		t.Fatalf("expected error for invalid hostname")
	}
}

func TestPanelSelfSign_InvalidIP(t *testing.T) {
	setupPanelSelfSign(t)
	if _, err := callPanelSelfSign(t, "host.example.com", "999.999.1.1"); err == nil {
		t.Fatalf("expected error for invalid ip")
	}
}
