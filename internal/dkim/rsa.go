package dkim

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// RSA DKIM keys (GH #2475). An Ed25519 signature alone is not enough: several
// large receivers and most checkers do not verify Ed25519 (RFC 8463), so a
// message signed only with it has no usable DKIM signature there and fails
// DKIM alignment. Every mail domain is therefore signed twice (RFC 8463 §4):
// with its Ed25519 key on Selector and with an RSA key on RSASelector.
//
// On-disk format is the PKCS#8 PEM of the private key, written with
// WritePrivate to /etc/jabali-panel/dkim/<domain>.rsa.key. Stalwart takes
// the same PEM (PKCS#1 or PKCS#8) for a Dkim1RsaSha256 signature.

// Selector is the DKIM selector of the Ed25519 key.
const Selector = "jabali"

// RSASelector is the DKIM selector of the RSA key.
const RSASelector = "jabalirsa"

// RSABits is the modulus size of a generated RSA key. 2048 is what receivers
// expect today; its TXT value (~400 characters) needs splitting into
// 255-character strings in DNS.
const RSABits = 2048

// ErrInvalidRSAKey is returned when a key file or PEM is not an RSA private key.
var ErrInvalidRSAKey = errors.New("dkim: not a valid RSA private key")

// RSAKey is a generated RSA DKIM key: the on-disk private form and the DNS
// TXT form.
type RSAKey struct {
	// PrivatePEM is the PKCS#8 PEM ("PRIVATE KEY") of the private key.
	PrivatePEM []byte
	// PublicDKIMTxt is the TXT record value `v=DKIM1; k=rsa; p=<base64>`,
	// one string, no quotes, no trailing newline.
	PublicDKIMTxt []byte
}

// GenerateRSA creates a fresh RSABits-bit RSA key for DKIM signing.
func GenerateRSA() (RSAKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, RSABits)
	if err != nil {
		return RSAKey{}, fmt.Errorf("dkim: rsa.GenerateKey: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return RSAKey{}, fmt.Errorf("dkim: marshal PKCS#8: %w", err)
	}
	txt, err := rsaPublicTxt(&key.PublicKey)
	if err != nil {
		return RSAKey{}, err
	}
	return RSAKey{
		PrivatePEM:    pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		PublicDKIMTxt: txt,
	}, nil
}

// PublicRSADKIMTxtFromPEM re-derives the TXT record value from a stored
// private key PEM (PKCS#8, or PKCS#1 "RSA PRIVATE KEY"). Its output is
// byte-identical to RSAKey.PublicDKIMTxt for the same key.
func PublicRSADKIMTxtFromPEM(privPEM []byte) ([]byte, error) {
	key, err := parseRSAPrivatePEM(privPEM)
	if err != nil {
		return nil, err
	}
	return rsaPublicTxt(&key.PublicKey)
}

// LoadRSA reads a key file written by WritePrivate and returns its PEM after
// checking it holds an RSA private key.
func LoadRSA(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller-supplied path, mode 0600 on disk
	if err != nil {
		return nil, fmt.Errorf("dkim: read %s: %w", path, err)
	}
	if _, err := parseRSAPrivatePEM(data); err != nil {
		return nil, err
	}
	return data, nil
}

func parseRSAPrivatePEM(privPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(privPEM)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", ErrInvalidRSAKey)
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRSAKey, err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: PKCS#8 key is %T", ErrInvalidRSAKey, k)
		}
		return rk, nil
	case "RSA PRIVATE KEY":
		rk, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRSAKey, err)
		}
		return rk, nil
	}
	return nil, fmt.Errorf("%w: PEM block %q", ErrInvalidRSAKey, block.Type)
}

// rsaPublicTxt renders the DKIM TXT value: p= is the base64 of the
// SubjectPublicKeyInfo DER, the form receivers parse (RFC 6376 §3.6.1).
func rsaPublicTxt(pub *rsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("dkim: marshal RSA public key: %w", err)
	}
	return []byte("v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)), nil
}
