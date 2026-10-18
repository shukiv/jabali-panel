package panelcert

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Certificate types, as the admin's Panel SSL view names them (GH #2166).
const (
	// TypeSelfSigned is the panel's own certificate: issuer Organization
	// SelfSignOrg or none. It is what Check regenerates.
	TypeSelfSigned = "self_signed"
	// TypeLetsEncrypt and TypeLetsEncryptStaging were issued by Let's
	// Encrypt's production and staging CAs.
	TypeLetsEncrypt        = "lets_encrypt"
	TypeLetsEncryptStaging = "lets_encrypt_staging"
	// TypeCA was issued by another CA, e.g. a custom upload.
	TypeCA = "ca"
)

// Details describes a certificate the panel serves, for the admin's Panel
// SSL view. It holds only public certificate data.
type Details struct {
	Type string `json:"type"`
	// Issuer is the issuer Organization, or its CommonName when it has none.
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	// Names are the DNS names, then the IP addresses, the certificate covers.
	Names     []string  `json:"names"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	// FingerprintSHA256 is the SHA-256 of the DER, as browsers show it.
	FingerprintSHA256 string `json:"fingerprint_sha256"`
}

// ErrNotCertificate is Describe's answer for data with no certificate.
var ErrNotCertificate = errors.New("not a PEM certificate")

// Describe reads the first certificate in certPEM: the leaf of a full chain.
func Describe(certPEM []byte) (*Details, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, ErrNotCertificate
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	org := IssuerOrg(cert)
	d := &Details{
		Type:              certType(org),
		Issuer:            org,
		Subject:           cert.Subject.CommonName,
		Names:             make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses)),
		NotBefore:         cert.NotBefore,
		NotAfter:          cert.NotAfter,
		FingerprintSHA256: fingerprint(block.Bytes),
	}
	if d.Issuer == "" {
		d.Issuer = strings.TrimSpace(cert.Issuer.CommonName)
	}
	d.Names = append(d.Names, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		d.Names = append(d.Names, ip.String())
	}
	return d, nil
}

// certType follows Check: an issuer Organization of SelfSignOrg or none is
// the panel's own certificate, anything else a real CA's.
func certType(org string) string {
	switch {
	case org == "" || org == SelfSignOrg:
		return TypeSelfSigned
	case strings.Contains(org, "Let's Encrypt") && strings.Contains(org, "STAGING"):
		return TypeLetsEncryptStaging
	case strings.Contains(org, "Let's Encrypt"):
		return TypeLetsEncrypt
	default:
		return TypeCA
	}
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
