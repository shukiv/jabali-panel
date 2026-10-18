// Package panelcert decides what the panel's self-signed mode does with the
// hostname certificate at /etc/jabali/tls/panel.crt (JAB-389, GH #2166). The
// panel reconciler, the admin's "Regenerate self-signed" action and the
// agent's ssl.panel.selfsign all classify the certificate with Check, so the
// panel can never answer "regenerating" while the agent answers "kept".
//
// Self-signed mode is "Use Let's Encrypt for the panel" off. Nothing here runs
// while it is on.
package panelcert

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"time"
)

// DefaultCertPath is where the panel's hostname certificate lives.
const DefaultCertPath = "/etc/jabali/tls/panel.crt"

// DefaultMailCertPath is where the panel's mail certificate lives, beside it
// (ADR-0105).
const DefaultMailCertPath = "/etc/jabali/tls/panel-mail.crt"

// SelfSignOrg is the issuer Organization of the panel's own self-signed
// certificate (ssl.panel.selfsign, and install.sh provision_tls_cert's
// "/O=Jabali Panel"). A certificate with another, non-empty issuer
// Organization was issued by a real CA (Let's Encrypt, or a custom upload).
const SelfSignOrg = "Jabali Panel"

// Action is what self-signed mode does with the current certificate.
type Action int

const (
	// Regenerate: write a new self-signed certificate for the hostname.
	Regenerate Action = iota
	// UpToDate: the self-signed certificate already covers the hostname and
	// hasn't expired.
	UpToDate
	// KeepCA: a real CA's certificate that hasn't expired. It is never
	// replaced: the 2026-05-09 incident clobbered a live Let's Encrypt
	// certificate with a self-signed one. Its names aren't checked, since a
	// custom wildcard certificate can cover the hostname without naming it.
	KeepCA
)

// Verdict is Check's answer.
type Verdict struct {
	Action Action
	// Reason says why, for logs and for the admin.
	Reason string
	// Issuer is the issuer Organization of the current certificate, "" when
	// it has none or couldn't be read.
	Issuer string
	// NotAfter is the current certificate's expiry; zero when it couldn't
	// be read.
	NotAfter time.Time
}

// Check classifies certPEM, the current certificate (nil or empty when the
// file is missing or unreadable), for hostname at now.
//
// An expired real-CA certificate is replaced (GH #2166): turning Let's Encrypt
// off keeps its certificate until it expires, not forever, and an expired
// certificate is no live certificate to protect. With Let's Encrypt off its
// renewals can also fail for good, e.g. behind a Cloudflare proxy.
func Check(certPEM []byte, hostname string, now time.Time) Verdict {
	if len(certPEM) == 0 {
		return Verdict{Action: Regenerate, Reason: "cert missing or unreadable"}
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return Verdict{Action: Regenerate, Reason: "cert not PEM"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Verdict{Action: Regenerate, Reason: "cert unparseable"}
	}
	v := Verdict{Issuer: IssuerOrg(cert), NotAfter: cert.NotAfter}
	expired := !cert.NotAfter.After(now)
	if v.Issuer != "" && v.Issuer != SelfSignOrg {
		if expired {
			v.Action, v.Reason = Regenerate, "cert issued by "+v.Issuer+" expired"
			return v
		}
		v.Action, v.Reason = KeepCA, "cert issued by "+v.Issuer
		return v
	}
	if !strings.EqualFold(cert.Subject.CommonName, hostname) {
		v.Action, v.Reason = Regenerate, "CN "+cert.Subject.CommonName+" != "+hostname
		return v
	}
	for _, need := range []string{hostname, "mail." + hostname} {
		if !hasDNSName(cert, need) {
			v.Action, v.Reason = Regenerate, "missing SAN "+need
			return v
		}
	}
	if expired {
		v.Action, v.Reason = Regenerate, "cert expired"
		return v
	}
	v.Action, v.Reason = UpToDate, "up to date for "+hostname
	return v
}

// IssuerOrg returns the certificate's first issuer Organization, trimmed, or
// "" when it has none.
func IssuerOrg(cert *x509.Certificate) string {
	if len(cert.Issuer.Organization) > 0 {
		return strings.TrimSpace(cert.Issuer.Organization[0])
	}
	return ""
}

func hasDNSName(cert *x509.Certificate, name string) bool {
	for _, d := range cert.DNSNames {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}
