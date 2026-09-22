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

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func writeCertLineage(t *testing.T, leRoot, name string, dnsNames []string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := filepath.Join(leRoot, "live", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), pemBytes, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
}

func callCertInfo(t *testing.T, params string) (sslCertInfoResponse, error) {
	t.Helper()
	raw, err := sslCertInfoHandler(context.Background(), json.RawMessage(params))
	if err != nil {
		return sslCertInfoResponse{}, err
	}
	b, _ := json.Marshal(raw)
	var resp sslCertInfoResponse
	if uErr := json.Unmarshal(b, &resp); uErr != nil {
		t.Fatalf("unmarshal response: %v", uErr)
	}
	return resp, nil
}

func TestSSLCertInfoHandler(t *testing.T) {
	orig := sslLERoot
	tmp := t.TempDir()
	sslLERoot = tmp
	t.Cleanup(func() { sslLERoot = orig })
	writeCertLineage(t, tmp, "example.com", []string{"example.com", "www.example.com"})

	// Exists and covers the requested SANs.
	resp, err := callCertInfo(t, `{"cert_name":"example.com","sans":["example.com","www.example.com"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Exists {
		t.Fatal("expected exists=true for a written lineage")
	}
	if !resp.CoversSANs {
		t.Errorf("expected covers_sans=true, dns_names=%v", resp.DNSNames)
	}
	if resp.NotBefore == "" || resp.NotAfter == "" {
		t.Errorf("expected not_before/not_after to be set, got %q / %q", resp.NotBefore, resp.NotAfter)
	}
	if resp.CertPath != filepath.Join(tmp, "live", "example.com", "fullchain.pem") {
		t.Errorf("unexpected cert_path %q", resp.CertPath)
	}

	// A required SAN the cert does not carry → covers_sans=false (still exists).
	resp, err = callCertInfo(t, `{"cert_name":"example.com","sans":["example.com","mail.example.com"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Exists || resp.CoversSANs {
		t.Errorf("a missing SAN must yield exists=true, covers_sans=false; got exists=%v covers=%v", resp.Exists, resp.CoversSANs)
	}

	// No lineage on disk → exists=false, NOT an error.
	resp, err = callCertInfo(t, `{"cert_name":"absent.com","sans":["absent.com"]}`)
	if err != nil {
		t.Fatalf("a missing lineage must not error: %v", err)
	}
	if resp.Exists {
		t.Error("expected exists=false for a lineage that was never issued")
	}

	// A cert_name with a path separator/wildcard must be rejected before any
	// read — no climbing out of <sslLERoot>/live/ into an arbitrary file.
	for _, bad := range []string{`{"cert_name":"../../etc/passwd"}`, `{"cert_name":"a/b"}`, `{"cert_name":"*.example.com"}`, `{"cert_name":""}`} {
		if _, err := sslCertInfoHandler(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("expected %s to be rejected", bad)
		} else if ae, ok := err.(*agentwire.AgentError); !ok || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("expected invalid_argument for %s, got %v", bad, err)
		}
	}
}
