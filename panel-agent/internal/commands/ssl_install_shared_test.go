package commands

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func genTestCertKey(t *testing.T, dnsNames []string, cn string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return
}

const testSharedULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// JAB-170 phase 2: install writes the pair under sslSharedRoot/<id> with a
// root-only key, reports deduped+lowercased SANs and an expiry; delete removes
// it.
func TestSSLInstallShared_WritesAndReports(t *testing.T) {
	tmp := t.TempDir()
	old := sslSharedRoot
	sslSharedRoot = tmp
	defer func() { sslSharedRoot = old }()

	certPEM, keyPEM := genTestCertKey(t, []string{"*.example.com", "Example.com"}, "example.com")
	raw, _ := json.Marshal(map[string]any{"id": testSharedULID, "cert_pem": certPEM, "key_pem": keyPEM})
	out, err := sslInstallSharedHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	resp, ok := out.(sslInstallSharedResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", out)
	}

	if _, e := os.Stat(filepath.Join(tmp, testSharedULID, "fullchain.pem")); e != nil {
		t.Error("cert file not written")
	}
	ki, e := os.Stat(filepath.Join(tmp, testSharedULID, "privkey.pem"))
	if e != nil {
		t.Fatalf("key file not written: %v", e)
	}
	if ki.Mode().Perm() != 0o600 {
		t.Errorf("key perm = %o, want 0600", ki.Mode().Perm())
	}
	if !reflect.DeepEqual(resp.SANs, []string{"*.example.com", "example.com"}) {
		t.Errorf("SANs = %v, want [*.example.com example.com]", resp.SANs)
	}
	if resp.ExpiresAt == "" {
		t.Error("empty ExpiresAt")
	}

	// delete → dir gone; idempotent on a second delete.
	draw, _ := json.Marshal(map[string]any{"id": testSharedULID})
	if _, e := sslDeleteSharedHandler(context.Background(), draw); e != nil {
		t.Fatalf("delete: %v", e)
	}
	if _, e := os.Stat(filepath.Join(tmp, testSharedULID)); !os.IsNotExist(e) {
		t.Error("dir not removed")
	}
	if _, e := sslDeleteSharedHandler(context.Background(), draw); e != nil {
		t.Errorf("second delete should be idempotent, got %v", e)
	}
}

// Bad id, empty cert, and a key that doesn't pair with the cert are all rejected
// before any write.
func TestSSLInstallShared_Rejections(t *testing.T) {
	tmp := t.TempDir()
	old := sslSharedRoot
	sslSharedRoot = tmp
	defer func() { sslSharedRoot = old }()

	certPEM, keyPEM := genTestCertKey(t, []string{"a.com"}, "a.com")
	_, otherKey := genTestCertKey(t, []string{"b.com"}, "b.com")
	for _, c := range []struct{ name, id, cert, key string }{
		{"bad id (path escape)", "../../etc/passwd", certPEM, keyPEM},
		{"non-ulid id", "not-a-ulid", certPEM, keyPEM},
		{"empty cert", testSharedULID, "", keyPEM},
		{"mismatched key", testSharedULID, certPEM, otherKey},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"id": c.id, "cert_pem": c.cert, "key_pem": c.key})
			if _, err := sslInstallSharedHandler(context.Background(), raw); err == nil {
				t.Errorf("expected rejection")
			}
		})
	}
	// nothing should have been written.
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("rejections wrote files: %v", ents)
	}
}
