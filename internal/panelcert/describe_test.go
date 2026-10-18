package panelcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDescribe_Type(t *testing.T) {
	later := time.Now().AddDate(1, 0, 0)
	for _, tc := range []struct {
		org, wantType, wantIssuer string
	}{
		{SelfSignOrg, TypeSelfSigned, SelfSignOrg},
		// No issuer Organization: ours, as Check treats it; the issuer is
		// shown by its CommonName.
		{"", TypeSelfSigned, "panel.example.com"},
		{"Let's Encrypt", TypeLetsEncrypt, "Let's Encrypt"},
		{"(STAGING) Let's Encrypt", TypeLetsEncryptStaging, "(STAGING) Let's Encrypt"},
		{"Example Trust Services", TypeCA, "Example Trust Services"},
	} {
		d, err := Describe(makePEM(t, tc.org, "panel.example.com", []string{"panel.example.com"}, later))
		if err != nil {
			t.Fatalf("%q: %v", tc.org, err)
		}
		if d.Type != tc.wantType || d.Issuer != tc.wantIssuer {
			t.Errorf("%q: type %q issuer %q, want %q %q", tc.org, d.Type, d.Issuer, tc.wantType, tc.wantIssuer)
		}
	}
}

func TestDescribe_Fields(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notBefore := time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("x", 3*3600))
	notAfter := notBefore.AddDate(10, 0, 0)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "panel.example.com", Organization: []string{SelfSignOrg}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{"panel.example.com", "mail.panel.example.com", "localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("203.0.113.10")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Describe(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "panel.example.com" {
		t.Errorf("subject %q", d.Subject)
	}
	wantNames := "panel.example.com mail.panel.example.com localhost 127.0.0.1 203.0.113.10"
	if got := strings.Join(d.Names, " "); got != wantNames {
		t.Errorf("names %q, want %q", got, wantNames)
	}
	if !d.NotBefore.Equal(notBefore) || !d.NotAfter.Equal(notAfter) || d.NotAfter.Location() != time.UTC {
		t.Errorf("validity %v – %v, want %v – %v in UTC", d.NotBefore, d.NotAfter, notBefore, notAfter)
	}
	sum := sha256.Sum256(der)
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if strings.ReplaceAll(d.FingerprintSHA256, ":", "") != want || len(d.FingerprintSHA256) != 95 {
		t.Errorf("fingerprint %q, want the colon-separated %q", d.FingerprintSHA256, want)
	}
}

// A full chain describes its leaf, the first certificate.
func TestDescribe_FullChainLeaf(t *testing.T) {
	later := time.Now().AddDate(0, 3, 0)
	leaf := makePEM(t, "Let's Encrypt", "panel.example.com", []string{"panel.example.com"}, later)
	inter := makePEM(t, "Internet Security Research Group", "R10", nil, later.AddDate(2, 0, 0))
	d, err := Describe(append(leaf, inter...))
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "panel.example.com" || d.Type != TypeLetsEncrypt {
		t.Errorf("got %q %q, want the leaf", d.Subject, d.Type)
	}
}

// Only a certificate is described: never a key, nor garbage.
func TestDescribe_NotACertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"empty":   nil,
		"garbage": []byte("not a cert"),
		"key":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if d, err := Describe(data); !errors.Is(err, ErrNotCertificate) || d != nil {
			t.Errorf("%s: got %v, %v; want ErrNotCertificate", name, d, err)
		}
	}
	if _, err := Describe(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")})); err == nil {
		t.Error("an unparseable certificate block was described")
	}
}
