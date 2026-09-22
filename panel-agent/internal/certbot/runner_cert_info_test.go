package certbot

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestLineage writes a self-signed leaf at <leRoot>/live/<name>/fullchain.pem
// so ReadLineageCert has a real PEM to parse.
func writeTestLineage(t *testing.T, leRoot, name string, notBefore, notAfter time.Time, dnsNames []string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1234567),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
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

func TestReadLineageCert(t *testing.T) {
	le := t.TempDir()
	nb := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)
	na := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	writeTestLineage(t, le, "example.com", nb, na, []string{"example.com", "www.example.com"})

	cert, err := ReadLineageCert(le, "example.com")
	if err != nil {
		t.Fatalf("ReadLineageCert: %v", err)
	}
	if !cert.NotBefore.Equal(nb) {
		t.Errorf("NotBefore = %v, want %v", cert.NotBefore, nb)
	}
	if !cert.NotAfter.Equal(na) {
		t.Errorf("NotAfter = %v, want %v", cert.NotAfter, na)
	}
	if len(cert.DNSNames) != 2 || cert.DNSNames[0] != "example.com" || cert.DNSNames[1] != "www.example.com" {
		t.Errorf("DNSNames = %v", cert.DNSNames)
	}

	// A lineage that does not exist is an error (callers treat it as "no cert").
	if _, err := ReadLineageCert(le, "absent.com"); err == nil {
		t.Error("missing lineage must return an error")
	}
}
