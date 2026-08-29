package api

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
	"strings"
	"testing"
	"time"
)

// GH #1355: parseSSLCertFile must surface the leaf's public details + PEM.
func TestParseSSLCertFile(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x0123456789),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com", "www.example.com", "mail.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fullchain.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := parseSSLCertFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(d.Subject, "example.com") {
		t.Errorf("subject = %q", d.Subject)
	}
	// Self-signed → issuer == subject.
	if !strings.Contains(d.Issuer, "example.com") {
		t.Errorf("issuer = %q", d.Issuer)
	}
	if len(d.SANs) != 3 || d.SANs[0] != "example.com" {
		t.Errorf("sans = %v", d.SANs)
	}
	if d.SHA256Fingerprint == "" || !strings.Contains(d.SHA256Fingerprint, ":") {
		t.Errorf("fingerprint = %q (want colon-hex)", d.SHA256Fingerprint)
	}
	if d.SerialNumber == "" {
		t.Error("serial empty")
	}
	if !strings.Contains(d.PEM, "BEGIN CERTIFICATE") {
		t.Error("PEM missing")
	}
}

func TestParseSSLCertFile_BadPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseSSLCertFile(path); err == nil {
		t.Fatal("expected error on non-PEM input")
	}
}
