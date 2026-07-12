package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// genTestCertPEM produces a self-signed leaf covering the given DNS name, plus
// its NotAfter, for exercising cliParseLeafNotAfter without a real CA.
func genTestCertPEM(t *testing.T, dnsName string) ([]byte, time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	notAfter := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), notAfter
}

func TestCLIParseLeafNotAfter(t *testing.T) {
	certPEM, want := genTestCertPEM(t, "example.com")

	leaf, notAfter, err := cliParseLeafNotAfter(certPEM)
	if err != nil {
		t.Fatalf("parse valid cert: %v", err)
	}
	if !notAfter.Equal(want) {
		t.Errorf("notAfter = %v, want %v", notAfter, want)
	}
	if leaf.VerifyHostname("example.com") != nil {
		t.Errorf("leaf should cover example.com")
	}
	if leaf.VerifyHostname("other.com") == nil {
		t.Errorf("leaf should NOT cover other.com")
	}
}

func TestCLIParseLeafNotAfter_Errors(t *testing.T) {
	// No CERTIFICATE block.
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
	if _, _, err := cliParseLeafNotAfter(keyBlock); err == nil {
		t.Errorf("expected error for PEM with no CERTIFICATE block")
	}
	// Garbage / empty.
	if _, _, err := cliParseLeafNotAfter([]byte("not pem")); err == nil {
		t.Errorf("expected error for non-PEM input")
	}
	// CERTIFICATE block with undecodable DER.
	bad := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})
	if _, _, err := cliParseLeafNotAfter(bad); err == nil {
		t.Errorf("expected error for malformed certificate DER")
	}
}
