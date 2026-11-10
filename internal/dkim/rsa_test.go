package dkim

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The TXT value carries a 2048-bit RSA key as base64 SPKI, and it verifies a
// signature made with the private key: what a receiver does with it.
func TestGenerateRSA_KeyShapeAndVerifies(t *testing.T) {
	k, err := GenerateRSA()
	if err != nil {
		t.Fatal(err)
	}
	txt := string(k.PublicDKIMTxt)
	const prefix = "v=DKIM1; k=rsa; p="
	if !strings.HasPrefix(txt, prefix) {
		t.Fatalf("TXT = %q, want prefix %q", txt, prefix)
	}
	der, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(txt, prefix))
	if err != nil {
		t.Fatalf("p= is not base64: %v", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("p= is not SPKI: %v", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() != RSABits {
		t.Fatalf("public key = %T, want RSA %d", pubAny, RSABits)
	}

	block, _ := pem.Decode(k.PrivatePEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("private key is not a PKCS#8 PEM: %q", k.PrivatePEM)
	}
	privAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("message"))
	sig, err := rsa.SignPKCS1v15(nil, privAny.(*rsa.PrivateKey), crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("the published key does not verify the private key's signature: %v", err)
	}
}

func TestPublicRSADKIMTxtFromPEM_MatchesGenerated(t *testing.T) {
	k, err := GenerateRSA()
	if err != nil {
		t.Fatal(err)
	}
	got, err := PublicRSADKIMTxtFromPEM(k.PrivatePEM)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(k.PublicDKIMTxt) {
		t.Fatalf("re-derived TXT differs:\n got %s\nwant %s", got, k.PublicDKIMTxt)
	}

	// A PKCS#1 PEM of the same key gives the same TXT.
	block, _ := pem.Decode(k.PrivatePEM)
	priv, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv.(*rsa.PrivateKey))})
	got, err = PublicRSADKIMTxtFromPEM(pkcs1)
	if err != nil || string(got) != string(k.PublicDKIMTxt) {
		t.Fatalf("PKCS#1 PEM: got %s, %v", got, err)
	}
}

func TestLoadRSA_RoundTripAndRejects(t *testing.T) {
	dir := t.TempDir()
	k, err := GenerateRSA()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "example.com.rsa.key")
	if err := WritePrivate(path, k.PrivatePEM); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := LoadRSA(path)
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := PublicRSADKIMTxtFromPEM(got); string(txt) != string(k.PublicDKIMTxt) {
		t.Fatal("a loaded key renders a different TXT")
	}

	ed, err := GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := base64.StdEncoding.DecodeString(string(ed.PrivateRawBase64))
	edPEM, _ := SeedToPKCS8PEM(seed)
	for name, content := range map[string][]byte{
		"ed25519 seed": ed.PrivateRawBase64,
		"ed25519 PEM":  edPEM,
		"garbage":      []byte("not a key"),
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRSA(p); !errors.Is(err, ErrInvalidRSAKey) {
			t.Errorf("%s: err = %v, want ErrInvalidRSAKey", name, err)
		}
	}
}
