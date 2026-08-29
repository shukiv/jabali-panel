package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// sslCertDetails is the parsed X.509 leaf shown by the SSL Manager's "View
// certificate" action (GH #1355). Everything here is public certificate data —
// no private key material is read or returned.
type sslCertDetails struct {
	Subject            string    `json:"subject"`
	Issuer             string    `json:"issuer"`
	SANs               []string  `json:"sans"`
	NotBefore          time.Time `json:"not_before"`
	NotAfter           time.Time `json:"not_after"`
	SerialNumber       string    `json:"serial_number"`
	SHA256Fingerprint  string    `json:"sha256_fingerprint"`
	SignatureAlgorithm string    `json:"signature_algorithm"`
	PublicKeyAlgorithm string    `json:"public_key_algorithm"`
	IsCA               bool      `json:"is_ca"`
	PEM                string    `json:"pem"`
}

// inspectSSL parses and returns the domain's issued certificate (GET
// /domains/:id/ssl/certificate). Admin or the domain owner. Only meaningful for
// a cert that exists on disk; a non-issued / fileless cert is a clean 409.
func (h *sslHandler) inspectSSL(c *gin.Context) {
	domainID := c.Param("id")
	if h.loadDomainOwned(c, domainID) == nil {
		return // loadDomainOwned already wrote the 401/403/404
	}

	cert, err := h.cfg.SSLCerts.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no_certificate"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if cert.Status != models.SSLStatusIssued || cert.CertPath == nil || *cert.CertPath == "" {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "no_certificate_on_disk",
			"detail": "This domain has no issued certificate to view yet (status: " + cert.Status + ").",
		})
		return
	}

	details, err := parseSSLCertFile(*cert.CertPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "parse_failed"})
		return
	}
	c.JSON(http.StatusOK, details)
}

// parseSSLCertFile reads a PEM cert file and returns the leaf's public details
// plus the file's PEM (the full chain, public). The first CERTIFICATE block is
// the leaf (same convention as the reconciler's certDNSNames / certNotAfter).
func parseSSLCertFile(path string) (*sslCertDetails, error) {
	rawFile, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(rawFile)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, os.ErrInvalid
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(leaf.Raw)
	return &sslCertDetails{
		Subject:            leaf.Subject.String(),
		Issuer:             leaf.Issuer.String(),
		SANs:               leaf.DNSNames,
		NotBefore:          leaf.NotBefore,
		NotAfter:           leaf.NotAfter,
		SerialNumber:       colonHex(leaf.SerialNumber.Bytes()),
		SHA256Fingerprint:  colonHex(sum[:]),
		SignatureAlgorithm: leaf.SignatureAlgorithm.String(),
		PublicKeyAlgorithm: leaf.PublicKeyAlgorithm.String(),
		IsCA:               leaf.IsCA,
		PEM:                string(rawFile),
	}, nil
}

// colonHex renders bytes as AA:BB:CC… (the conventional fingerprint form).
func colonHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = hex.EncodeToString([]byte{x})
	}
	return strings.ToUpper(strings.Join(parts, ":"))
}
